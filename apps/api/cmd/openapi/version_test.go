package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractVersionMatchesPublishedContract(t *testing.T) {
	root := filepath.Join("..", "..")
	spec, err := os.ReadFile(filepath.Join(root, "internal/apidocs/openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := readVersionRecord(filepath.Join(root, versionPath))
	if err != nil || recorded == nil {
		t.Fatalf("version record: %v %v", recorded, err)
	}
	digest, err := contractDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Version != contractVersion || recorded.SHA256 != digest {
		t.Fatalf("record %+v, want version %s sha %s", recorded, contractVersion, digest)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatal(err)
	}
	if v := doc["info"].(map[string]interface{})["version"]; v != contractVersion {
		t.Fatalf("info.version = %v, want %s", v, contractVersion)
	}
}

func TestContractVersionGateMutation(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "internal/apidocs/openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := contractDigest(spec)
	recorded := &versionRecord{Version: "2026-10-07", SHA256: base}

	// Unchanged contract passes.
	if err := versionGate(recorded, "2026-10-07", base); err != nil {
		t.Fatalf("unchanged contract: %v", err)
	}

	// Info-only edits do not count as a contract change.
	var doc map[string]interface{}
	_ = json.Unmarshal(spec, &doc)
	doc["info"].(map[string]interface{})["description"] = "edited"
	doc["info"].(map[string]interface{})["version"] = "2099-01-01"
	infoOnly, _ := json.Marshal(doc)
	if d, _ := contractDigest(infoOnly); d != base {
		t.Fatal("info edits changed the contract digest")
	}

	// A DTO change without a bump fails with the bump message.
	_ = json.Unmarshal(spec, &doc)
	schemas := doc["components"].(map[string]interface{})["schemas"].(map[string]interface{})
	props := schemas["model.UpdateLabelRequest"].(map[string]interface{})["properties"].(map[string]interface{})
	props["mutationProbe"] = map[string]interface{}{"type": "string"}
	mutated, _ := json.Marshal(doc)
	changed, _ := contractDigest(mutated)
	if changed == base {
		t.Fatal("DTO change did not change the digest")
	}
	if err := versionGate(recorded, "2026-10-07", changed); err == nil || !strings.Contains(err.Error(), "contract changed: bump contractVersion") {
		t.Fatalf("unbumped change: %v", err)
	}

	// Bumping the version accepts the change.
	if err := versionGate(recorded, "2026-10-08", changed); err != nil {
		t.Fatalf("bumped change: %v", err)
	}

	// The version never moves backwards and must be a date.
	if err := versionGate(recorded, "2026-10-06", changed); err == nil {
		t.Fatal("older version accepted")
	}
	if err := versionGate(nil, "v2", base); err == nil {
		t.Fatal("non-date version accepted")
	}
	if err := versionGate(nil, "2026-10-07", base); err != nil {
		t.Fatalf("first generation: %v", err)
	}
}
