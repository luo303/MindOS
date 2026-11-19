package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRawJSONMarshalsAsJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  RawJSON
		want string
	}{
		{name: "array", raw: RawJSON(`[{"n":1,"chunk_id":2}]`), want: `{"citations":[{"n":1,"chunk_id":2}]}`},
		{name: "empty", raw: nil, want: `{"citations":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(struct {
				Citations RawJSON `json:"citations"`
			}{Citations: tc.raw})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("json = %s, want %s", data, tc.want)
			}
		})
	}
}

func TestRawJSONRejectsInvalid(t *testing.T) {
	if _, err := json.Marshal(RawJSON(`{not json}`)); err == nil {
		t.Fatal("expected invalid raw json to error")
	} else if !strings.Contains(err.Error(), "invalid raw json") {
		t.Fatalf("error = %v, want invalid raw json", err)
	}
}

func TestMessageCitationsJSONIsArray(t *testing.T) {
	msg := Message{
		ID:        1,
		Role:      RoleAssistant,
		Content:   "见 [1]",
		Citations: RawJSON(`[{"n":1,"chunk_id":2,"document_id":3,"seq":0,"title":"文档"}]`),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"citations":[{`) {
		t.Fatalf("citations is not a JSON array: %s", body)
	}
	if strings.Contains(body, "eyJ") {
		t.Fatalf("citations looks like base64: %s", body)
	}
}
