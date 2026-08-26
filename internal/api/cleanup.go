package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"flatline/internal/assets"
	"flatline/internal/vital"
)

type cleanupCandidate struct {
	Asset         assetListItem        `json:"asset"`
	Reason        string               `json:"reason"`
	Rollback      vital.RollbackRecord `json:"rollback"`
	StateInstance int64                `json:"state_instance_id"`
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	items, err := s.listAssets(r.Context())
	if err != nil {
		http.Error(w, "cleanup query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]cleanupCandidate, 0)
	for _, item := range items {
		if item.CurrentState == nil || item.CurrentState.State != vital.StateDormant {
			continue
		}
		rollback := vital.RollbackRecord{Reversible: item.SourcePath != nil && strings.TrimSpace(*item.SourcePath) != "", Strategy: "保留源文件；仅撤销逻辑归档标记"}
		if item.SourcePath != nil {
			rollback.SourcePath = *item.SourcePath
		}
		out = append(out, cleanupCandidate{Asset: item, Reason: "资产已达到休眠判定，当前参与次数不超过阈值", Rollback: rollback, StateInstance: item.CurrentState.InstanceID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

func (s *Server) handleBatchCleanup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AssetIDs  []string `json:"asset_ids"`
		Confirmed bool     `json:"confirmed"`
		Reason    string   `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid cleanup request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !request.Confirmed {
		http.Error(w, "batch cleanup requires explicit confirmation", http.StatusBadRequest)
		return
	}
	if len(request.AssetIDs) == 0 {
		http.Error(w, "asset_ids is required", http.StatusBadRequest)
		return
	}
	type pending struct {
		assetID string
		stateID int64
		path    string
	}
	pendingItems := make([]pending, 0, len(request.AssetIDs))
	seen := make(map[string]struct{}, len(request.AssetIDs))
	for _, assetID := range request.AssetIDs {
		if _, ok := seen[assetID]; ok {
			continue
		}
		seen[assetID] = struct{}{}
		asset, err := assets.New(s.db).Get(r.Context(), assetID)
		if err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "asset not found: "+assetID, http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		current, err := s.vital.Current(r.Context(), assetID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if current == nil || current.State != vital.StateDormant {
			http.Error(w, "only dormant assets can enter batch cleanup: "+assetID, http.StatusConflict)
			return
		}
		if asset.SourcePath == nil || strings.TrimSpace(*asset.SourcePath) == "" {
			http.Error(w, "cleanup requires a recorded source path: "+assetID, http.StatusBadRequest)
			return
		}
		pendingItems = append(pendingItems, pending{assetID: assetID, stateID: current.InstanceID, path: *asset.SourcePath})
	}
	created := make([]vital.Disposition, 0, len(pendingItems))
	store := vital.NewDispositionStore(s.db, s.vital)
	for _, item := range pendingItems {
		disposition, err := store.Apply(r.Context(), vital.DispositionRequest{AssetID: item.assetID, Action: vital.ActionPrune, StateInstanceID: item.stateID, Confirmed: true, Reason: request.Reason, Rollback: vital.RollbackRecord{SourcePath: item.path, Strategy: "保留源文件；仅撤销逻辑归档标记", Reversible: true}})
		if err != nil {
			http.Error(w, "cleanup failed for "+item.assetID+": "+err.Error(), http.StatusConflict)
			return
		}
		created = append(created, *disposition)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"archived": created, "source_files_changed": false})
}
