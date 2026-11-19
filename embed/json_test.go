package embed

import "encoding/json"

func jsonMarshal(v any) (json.RawMessage, error) { return json.Marshal(v) }
