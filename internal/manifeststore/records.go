package manifeststore

import "encoding/json"

// RecordID extracts the "id" field from any raw manifest record.
func RecordID(raw json.RawMessage) string {
	var r struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.ID
}

// patchRecord applies key/value updates to a raw record, preserving every other
// field (modelled or not). A nil value deletes the key.
func patchRecord(raw json.RawMessage, patch map[string]any) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
	}
	for k, v := range patch {
		if v == nil {
			delete(obj, k)
			continue
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		obj[k] = enc
	}
	return json.Marshal(obj)
}

// decodeArray unmarshals a manifest array key into raw elements (nil-safe).
func decodeArray(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	return arr
}

// TrimJSON drops the trailing padding SealManifest adds so a strict unmarshal of
// the decrypted manifest succeeds.
func TrimJSON(b []byte) []byte {
	i := len(b)
	for i > 0 {
		switch b[i-1] {
		case ' ', '\n', '\t', '\r', 0:
			i--
			continue
		}
		break
	}
	return b[:i]
}
