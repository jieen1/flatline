package api

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"flatline/internal/friction"
)

// Coverage answers one factual question and stops there: for a friction
// signature whose mechanism is a rule the harness enforces, does any rule the
// user wrote for that project mention the mechanism at all?
//
// It never claims the missing rule caused the friction, and never claims
// writing one would end it (ADR-8). "No rule mentions this" and "this keeps
// happening" are two separate recorded facts, reported side by side.

const coverageGapNote = "（签名 × 项目）组合要三条同时成立才计入：同一项目里 ≥2 个会话记录过它；机制字典把它读成带关键词的 harness_rule；该项目适用的规则资产（用户级，加该项目目录下的 rule / agents_md）正文里一个关键词都没出现。按会话数降序取前 10。这句话只说“没提到”，不说“因此才发生”。"

const coverageGapNoteEN = "A signature-and-project pair counts only when all three hold: two or more sessions in that project recorded it; the dictionary reads its mechanism as a harness_rule that carries keywords; and no rule asset applicable to that project (user scope, plus rule / agents_md files under the project directory) mentions any of those keywords in its text. Top 10 by session count. It says nothing mentions the mechanism, never that this is why the friction happened."

const (
	// coverageGapMinSessions is what "recurring" means here: one session that
	// hit a signature once is not a pattern a rule could have been written for.
	coverageGapMinSessions = 2
	coverageGapLimit       = 10
	// coverageRuleReadLimit bounds how much of a rule file is scanned. A rule
	// nobody could read past 256 KB is not a rule the reader was following.
	coverageRuleReadLimit = 256 << 10
)

// frictionCoverageGap is one (signature × project) pair that recurs while no
// rule applicable to that project mentions its mechanism.
type frictionCoverageGap struct {
	Signature    string `json:"signature"`
	SampleLine   string `json:"sample_line"`
	ProjectKey   string `json:"project_key"`
	SessionCount int    `json:"session_count"`
	HintKind     string `json:"hint_kind"`
	Mechanism    string `json:"mechanism"`
	MechanismEN  string `json:"mechanism_en"`
}

// coverageRuleMentions is one rule asset reduced to the two things the
// question needs: where it applies, and which mechanism keywords its text
// contains.
type coverageRuleMentions struct {
	// userScope rules apply to every project; a project-scope rule applies
	// only under its own directory.
	userScope bool
	path      string
	keywords  map[string]struct{}
}

func (rule coverageRuleMentions) appliesTo(projectKey string) bool {
	if rule.userScope {
		return true
	}
	return pathUnder(rule.path, projectKey)
}

// coverageMentions is every rule asset's mentions, kept per rule rather than
// merged: merging them is what let one project's rules shield another's gaps.
type coverageMentions struct {
	rules []coverageRuleMentions
}

// covers reports whether a rule applicable to projectKey mentions any of the
// keywords. Keywords are already lower-cased by the caller side that stored
// them.
func (m coverageMentions) covers(projectKey string, keywords []string) bool {
	for _, rule := range m.rules {
		if !rule.appliesTo(projectKey) {
			continue
		}
		for _, keyword := range keywords {
			if _, ok := rule.keywords[strings.ToLower(keyword)]; ok {
				return true
			}
		}
	}
	return false
}

// pathUnder reports whether path lives inside dir. It is a path comparison,
// not a filesystem one: a project directory that no longer exists still owns
// the rules that were registered under it.
func pathUnder(path, dir string) bool {
	if path == "" || dir == "" || dir == frictionUnrecordedKey {
		return false
	}
	cleanDir := filepath.Clean(dir)
	cleanPath := filepath.Clean(path)
	if cleanDir == string(filepath.Separator) {
		return strings.HasPrefix(cleanPath, cleanDir)
	}
	return strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator))
}

// coverageCache holds one reading of the rule assets on disk. Reading 100+
// files per friction request is what it exists to avoid; the fingerprint is
// what makes it safe to keep.
type coverageCache struct {
	mu          sync.Mutex
	fingerprint string
	mentions    coverageMentions
	loaded      bool
}

func (c *coverageCache) load(fingerprint string) (coverageMentions, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || c.fingerprint != fingerprint {
		return coverageMentions{}, false
	}
	return c.mentions, true
}

func (c *coverageCache) store(fingerprint string, mentions coverageMentions) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fingerprint, c.mentions, c.loaded = fingerprint, mentions, true
}

// keywordCoverage reads which mechanism keywords each rule asset mentions,
// from the cache when the rule assets have not moved since it was filled.
func (s *Server) keywordCoverage(ctx context.Context) (coverageMentions, error) {
	fingerprint, err := s.coverageFingerprint(ctx)
	if err != nil {
		return coverageMentions{}, err
	}
	if mentions, ok := s.coverage.load(fingerprint); ok {
		return mentions, nil
	}
	mentions, err := s.readRuleMentions(ctx)
	if err != nil {
		return coverageMentions{}, err
	}
	s.coverage.store(fingerprint, mentions)
	return mentions, nil
}

// coverageFingerprint changes when a rule asset appears or disappears, and
// when any asset gets a new version — which is how an edited rule file
// announces itself, since the snapshotter versions content changes.
func (s *Server) coverageFingerprint(ctx context.Context) (string, error) {
	var assetCount, maxVersion int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assets WHERE kind IN ('rule', 'agents_md') AND archived_at IS NULL`,
	).Scan(&assetCount); err != nil {
		return "", fmt.Errorf("api: count rule assets: %w", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM asset_versions`,
	).Scan(&maxVersion); err != nil {
		return "", fmt.Errorf("api: read asset version high-water mark: %w", err)
	}
	return strconv.FormatInt(assetCount, 10) + ":" + strconv.FormatInt(maxVersion, 10), nil
}

// readRuleMentions opens every rule asset's source file read-only and records
// which keywords it contains. A file that cannot be read contributes no
// mentions, which leaves gaps visible rather than silently covered.
func (s *Server) readRuleMentions(ctx context.Context) (coverageMentions, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scope, COALESCE(source_path, '') FROM assets
		 WHERE kind IN ('rule', 'agents_md') AND archived_at IS NULL AND source_path IS NOT NULL
		 ORDER BY id`)
	if err != nil {
		return coverageMentions{}, fmt.Errorf("api: list rule assets: %w", err)
	}
	defer rows.Close()
	keywords := friction.AllCoverageKeywords()
	out := coverageMentions{rules: make([]coverageRuleMentions, 0, 32)}
	for rows.Next() {
		var scope, sourcePath string
		if err := rows.Scan(&scope, &sourcePath); err != nil {
			return coverageMentions{}, fmt.Errorf("api: scan rule asset: %w", err)
		}
		if strings.TrimSpace(sourcePath) == "" {
			continue
		}
		text, ok := readRuleText(sourcePath)
		if !ok {
			continue
		}
		mentioned := make(map[string]struct{})
		for _, keyword := range keywords {
			lowered := strings.ToLower(keyword)
			if lowered != "" && strings.Contains(text, lowered) {
				mentioned[lowered] = struct{}{}
			}
		}
		if len(mentioned) == 0 {
			continue
		}
		out.rules = append(out.rules, coverageRuleMentions{
			userScope: scope == "user", path: sourcePath, keywords: mentioned})
	}
	if err := rows.Err(); err != nil {
		return coverageMentions{}, fmt.Errorf("api: iterate rule assets: %w", err)
	}
	return out, nil
}

// readRuleText returns the lower-cased head of a rule file. The second return
// is false when the file is not readable at all.
func readRuleText(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, coverageRuleReadLimit))
	if err != nil {
		return "", false
	}
	return strings.ToLower(string(body)), true
}

// coverageGaps is the friction side of the question: which (signature ×
// project) pairs recur, carry a harness-rule mechanism with keywords, and are
// mentioned by no rule applicable to that project.
//
// A signature whose mechanism carries no keywords is skipped rather than
// reported: for those the question cannot be asked, which is not the same
// answer as "no rule mentions it".
func (set frictionSet) coverageGaps(mentions coverageMentions) []frictionCoverageGap {
	type pair struct{ signature, projectKey string }
	sessions := make(map[pair]map[string]struct{})
	for _, record := range set.records {
		if record.signature == "" {
			continue
		}
		key := pair{signature: record.signature, projectKey: record.projectKey}
		seen, ok := sessions[key]
		if !ok {
			seen = make(map[string]struct{})
			sessions[key] = seen
		}
		seen[record.sessionID] = struct{}{}
	}
	out := make([]frictionCoverageGap, 0, len(sessions))
	for key, seen := range sessions {
		if len(seen) < coverageGapMinSessions {
			continue
		}
		keywords := friction.CoverageKeywords(key.signature)
		if len(keywords) == 0 {
			continue
		}
		if mentions.covers(key.projectKey, keywords) {
			continue
		}
		gap := frictionCoverageGap{
			Signature: key.signature, SampleLine: frictionSignatureLine(key.signature),
			ProjectKey: key.projectKey, SessionCount: len(seen),
		}
		if hint := friction.LookupHint(key.signature); hint != nil {
			gap.HintKind, gap.Mechanism, gap.MechanismEN = hint.Kind, hint.Mechanism, hint.MechanismEN
		}
		out = append(out, gap)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SessionCount != out[j].SessionCount {
			return out[i].SessionCount > out[j].SessionCount
		}
		if out[i].Signature != out[j].Signature {
			return out[i].Signature < out[j].Signature
		}
		return out[i].ProjectKey < out[j].ProjectKey
	})
	if len(out) > coverageGapLimit {
		out = out[:coverageGapLimit]
	}
	return out
}

// frictionSummary is the summary every friction endpoint returns: the counts
// computed over the loaded set, plus the coverage question answered against
// the rule assets on disk.
func (s *Server) frictionSummary(ctx context.Context, set frictionSet) (frictionSummaryResponse, error) {
	out := set.summary()
	mentions, err := s.keywordCoverage(ctx)
	if err != nil {
		return frictionSummaryResponse{}, err
	}
	out.CoverageGaps = set.coverageGaps(mentions)
	out.CoverageGapNote, out.CoverageGapNoteEN = coverageGapNote, coverageGapNoteEN
	return out, nil
}
