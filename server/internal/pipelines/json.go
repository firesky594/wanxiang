package pipelines

import "encoding/json"

func jsonUnmarshal(v string, out any) error { return json.Unmarshal([]byte(v), out) }
