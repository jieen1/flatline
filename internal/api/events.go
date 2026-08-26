package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleSessionEvent(w http.ResponseWriter, r *http.Request) {
	eventID, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	row := s.db.QueryRowContext(r.Context(), `
		SELECT id, session_id, event_type, asset_id, asset_version_id, COALESCE(source_event_id, ''), participation_signal,
		       observation_level, COALESCE(payload_json, ''), COALESCE(locator_json, ''), COALESCE(occurred_at, ''), COALESCE(adapter_version, '')
		FROM events WHERE session_id = ? AND id = ?`, r.PathValue("id"), eventID)
	var id int64
	var sessionID, eventType, sourceID, observation, payload, locator, occurred, adapterVersion string
	var assetID, signal sql.NullString
	var versionID sql.NullInt64
	if err := row.Scan(&id, &sessionID, &eventType, &assetID, &versionID, &sourceID, &signal, &observation, &payload, &locator, &occurred, &adapterVersion); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": eventResponseFromFields(id, sessionID, eventType, assetID, versionID, sourceID, signal, observation, payload, locator, occurred, adapterVersion, false)})
}

func eventResponseFromFields(id int64, sessionID, eventType string, assetID sql.NullString, versionID sql.NullInt64, sourceID string, signal sql.NullString, observation, payload, locator, occurred, adapterVersion string, compact bool) map[string]any {
	payloadJSON := json.RawMessage(payload)
	locatorJSON := json.RawMessage(locator)
	payloadTruncated := false
	locatorTruncated := false
	if compact {
		payloadJSON, payloadTruncated = compactEventJSON(payload)
		locatorJSON, locatorTruncated = compactEventJSON(locator)
	}
	event := map[string]any{"id": id, "session_id": sessionID, "event_type": eventType, "observation_level": observation, "payload": payloadJSON, "locator": locatorJSON}
	if payloadTruncated {
		event["payload_truncated"] = true
	}
	if locatorTruncated {
		event["locator_truncated"] = true
	}
	if assetID.Valid {
		event["asset_id"] = assetID.String
	}
	if versionID.Valid {
		event["asset_version_id"] = versionID.Int64
	}
	if signal.Valid {
		event["participation_signal"] = signal.String
	}
	if occurred != "" {
		event["occurred_at"] = occurred
	}
	if adapterVersion != "" {
		event["adapter_version"] = adapterVersion
	}
	if sourceID != "" {
		event["source_event_id"] = sourceID
	}
	return event
}

func compactEventJSON(raw string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage(`null`), false
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return json.RawMessage(strconv.Quote(compactEventString(trimmed))), len(trimmed) > 512
	}
	compact, changed := compactEventValue(value)
	encoded, err := json.Marshal(compact)
	if err != nil {
		return json.RawMessage(`null`), true
	}
	return json.RawMessage(encoded), changed
}

func compactEventValue(value any) (any, bool) {
	switch item := value.(type) {
	case string:
		if len([]rune(item)) <= 512 {
			return item, false
		}
		return compactEventString(item), true
	case []any:
		changed := false
		for index := range item {
			compact, itemChanged := compactEventValue(item[index])
			item[index] = compact
			changed = changed || itemChanged
		}
		return item, changed
	case map[string]any:
		changed := false
		for key, nested := range item {
			compact, itemChanged := compactEventValue(nested)
			item[key] = compact
			changed = changed || itemChanged
		}
		return item, changed
	default:
		return value, false
	}
}

func compactEventString(value string) string {
	runes := []rune(value)
	if len(runes) <= 512 {
		return value
	}
	return string(runes[:512]) + "… [payload truncated; select the event to load the full local record]"
}
