package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// contractVersion is the published info.version. Rule: every change to the
// generated paths or components sets it to the date (YYYY-MM-DD) the change
// merges to main. Generation and --check refuse a changed contract whose
// version was not bumped, using the digest in versionPath.
const contractVersion = "2026-10-08"

const versionPath = "internal/apidocs/openapi.version.json"

type versionRecord struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// contractDigest hashes the canonical JSON of paths and components only, so
// info (title, version, description) and servers never change the digest.
func contractDigest(spec []byte) (string, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(spec, &doc); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(map[string]interface{}{"paths": doc["paths"], "components": doc["components"]})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// versionGate compares the current contract against the last recorded one.
// A nil record (first generation) always passes.
func versionGate(recorded *versionRecord, version, digest string) error {
	if _, err := time.Parse("2006-01-02", version); err != nil {
		return fmt.Errorf("contractVersion %q is not a YYYY-MM-DD date", version)
	}
	if recorded == nil {
		return nil
	}
	if version < recorded.Version {
		return fmt.Errorf("contractVersion %s is older than the recorded %s", version, recorded.Version)
	}
	if version == recorded.Version && digest != recorded.SHA256 {
		return fmt.Errorf("contract changed: bump contractVersion (still %s) in cmd/openapi/version.go to the merge date", version)
	}
	return nil
}

func readVersionRecord(path string) (*versionRecord, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r versionRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

func versionFile(digest string) []byte {
	b, err := json.MarshalIndent(versionRecord{Version: contractVersion, SHA256: digest}, "", "  ")
	must(err)
	return append(b, '\n')
}
