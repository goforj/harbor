package control

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestDecodeInspectProjectRuntimeRepairRequestRequiresOneBoundedProjectSelection rejects every additional source of repair authority.
func TestDecodeInspectProjectRuntimeRepairRequestRequiresOneBoundedProjectSelection(t *testing.T) {
	valid := []byte(`{"project_id":"project-orders"}`)
	want := InspectProjectRuntimeRepairRequest{ProjectID: "project-orders"}
	got, err := decodeInspectProjectRuntimeRepairRequest(valid)
	if err != nil || got != want {
		t.Fatalf("decodeInspectProjectRuntimeRepairRequest() = %#v, %v, want %#v", got, err, want)
	}

	bounded := append([]byte(nil), valid...)
	bounded = append(bounded, strings.Repeat(" ", maximumProjectRuntimeRepairInspectionRequestBytes-len(bounded))...)
	if _, err := decodeInspectProjectRuntimeRepairRequest(bounded); err != nil {
		t.Fatalf("decodeInspectProjectRuntimeRepairRequest(exact bound) error = %v", err)
	}
	if _, err := decodeInspectProjectRuntimeRepairRequest(append(bounded, ' ')); err == nil {
		t.Fatal("decodeInspectProjectRuntimeRepairRequest(over bound) error = nil")
	}

	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "empty"},
		{name: "invalid JSON", payload: `x`},
		{name: "null", payload: `null`},
		{name: "array", payload: `[]`},
		{name: "string", payload: `"project-orders"`},
		{name: "empty object", payload: `{}`},
		{name: "duplicate project", payload: `{"project_id":"project-orders","project_id":"project-other"}`},
		{name: "escaped duplicate project", payload: `{"project_id":"project-orders","project_\u0069d":"project-other"}`},
		{name: "unknown field", payload: `{"project_id":"project-orders","force":true}`},
		{name: "PID authority", payload: `{"project_id":"project-orders","root_pid":321}`},
		{name: "endpoint authority", payload: `{"project_id":"project-orders","endpoint":"127.0.0.1:3000"}`},
		{name: "wrong project type", payload: `{"project_id":7}`},
		{name: "null project", payload: `{"project_id":null}`},
		{name: "invalid project", payload: `{"project_id":" bad "}`},
		{name: "unterminated object", payload: `{"project_id":"project-orders"`},
		{name: "trailing object", payload: `{"project_id":"project-orders"}{}`},
		{name: "trailing token", payload: `{"project_id":"project-orders"} x`},
		{name: "oversized", payload: strings.Repeat(" ", maximumProjectRuntimeRepairInspectionRequestBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeInspectProjectRuntimeRepairRequest([]byte(test.payload)); err == nil {
				t.Fatalf("decodeInspectProjectRuntimeRepairRequest(%q) error = nil", test.payload)
			}
		})
	}
}

// TestDecodeConfirmProjectRuntimeRepairRequestRequiresOnlyOpaqueSelection proves clients cannot reconstruct or expand a retained plan.
func TestDecodeConfirmProjectRuntimeRepairRequestRequiresOnlyOpaqueSelection(t *testing.T) {
	inspectionID := strings.Repeat("a", projectRuntimeRepairOpaqueHexLength)
	fingerprint := strings.Repeat("b", projectRuntimeRepairOpaqueHexLength)
	valid := `{"candidate_fingerprint":"` + fingerprint + `","inspection_id":"` + inspectionID + `","project_id":"project-orders"}`
	want := ConfirmProjectRuntimeRepairRequest{
		ProjectID:    "project-orders",
		InspectionID: ProjectRuntimeRepairInspectionID(inspectionID),
		Fingerprint:  ProjectRuntimeRepairCandidateFingerprint(fingerprint),
	}
	got, err := decodeConfirmProjectRuntimeRepairRequest([]byte(valid))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeConfirmProjectRuntimeRepairRequest() = %#v, %v, want %#v", got, err, want)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(confirm request) error = %v", err)
	}
	wantEncoded := `{"project_id":"project-orders","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `"}`
	if string(encoded) != wantEncoded {
		t.Fatalf("confirm request JSON = %s, want %s", encoded, wantEncoded)
	}

	bounded := append([]byte(nil), valid...)
	bounded = append(bounded, strings.Repeat(" ", maximumProjectRuntimeRepairConfirmationRequestBytes-len(bounded))...)
	if _, err := decodeConfirmProjectRuntimeRepairRequest(bounded); err != nil {
		t.Fatalf("decodeConfirmProjectRuntimeRepairRequest(exact bound) error = %v", err)
	}
	if _, err := decodeConfirmProjectRuntimeRepairRequest(append(bounded, ' ')); err == nil {
		t.Fatal("decodeConfirmProjectRuntimeRepairRequest(over bound) error = nil")
	}

	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "empty"},
		{name: "invalid JSON", payload: `x`},
		{name: "null", payload: `null`},
		{name: "array", payload: `[]`},
		{name: "missing project", payload: `{"inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "missing inspection", payload: `{"project_id":"project-orders","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "missing fingerprint", payload: `{"project_id":"project-orders","inspection_id":"` + inspectionID + `"}`},
		{name: "old fingerprint name", payload: `{"project_id":"project-orders","inspection_id":"` + inspectionID + `","fingerprint":"` + fingerprint + `"}`},
		{name: "duplicate project", payload: `{"project_id":"project-orders","project_id":"project-other","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "duplicate inspection", payload: `{"project_id":"project-orders","inspection_id":"` + inspectionID + `","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "duplicate fingerprint", payload: `{"project_id":"project-orders","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "escaped duplicate fingerprint", payload: `{"project_id":"project-orders","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `","candidate_fingerpr\u0069nt":"` + fingerprint + `"}`},
		{name: "wrong project type", payload: `{"project_id":7,"inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "wrong inspection type", payload: `{"project_id":"project-orders","inspection_id":7,"candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "wrong fingerprint type", payload: `{"project_id":"project-orders","inspection_id":"` + inspectionID + `","candidate_fingerprint":{}}`},
		{name: "invalid project", payload: `{"project_id":" bad ","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "short inspection", payload: `{"project_id":"project-orders","inspection_id":"a","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "uppercase inspection", payload: `{"project_id":"project-orders","inspection_id":"` + strings.ToUpper(inspectionID) + `","candidate_fingerprint":"` + fingerprint + `"}`},
		{name: "nonhex fingerprint", payload: `{"project_id":"project-orders","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + strings.Repeat("g", projectRuntimeRepairOpaqueHexLength) + `"}`},
		{name: "unterminated object", payload: valid[:len(valid)-1]},
		{name: "trailing object", payload: valid + `{}`},
		{name: "trailing token", payload: valid + ` x`},
		{name: "oversized", payload: strings.Repeat(" ", maximumProjectRuntimeRepairConfirmationRequestBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeConfirmProjectRuntimeRepairRequest([]byte(test.payload)); err == nil {
				t.Fatalf("decodeConfirmProjectRuntimeRepairRequest(%q) error = nil", test.payload)
			}
		})
	}

	for _, field := range []string{
		`"command":"forj dev"`,
		`"checkout":"/workspace/orders"`,
		`"endpoint":"127.0.0.1:3000"`,
		`"root_pid":321`,
		`"member_count":4`,
		`"birth_token":"opaque-native-value"`,
		`"argv":["forj","dev"]`,
		`"environment":{"HARBOR":"value"}`,
		`"expected_project_revision":42`,
		`"expected_session_generation":3`,
		`"signal_scope":"process-group"`,
	} {
		payload := valid[:len(valid)-1] + `,` + field + `}`
		if _, err := decodeConfirmProjectRuntimeRepairRequest([]byte(payload)); err == nil {
			t.Errorf("decodeConfirmProjectRuntimeRepairRequest() accepted hidden authority field %s", field)
		}
	}
}
