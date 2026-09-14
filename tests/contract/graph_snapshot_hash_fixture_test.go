package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
)

type publicGraphHashCase struct {
	Name        string                  `json:"name"`
	RequestJSON string                  `json:"request_json"`
	Base        *graphsnapshot.Manifest `json:"base"`
	Reject      string                  `json:"reject"`
	Canonical   string                  `json:"expected_canonical"`
	Hash        string                  `json:"expected_hash"`
}

// These expectations are fixed public bytes, not regenerated using the
// provider. Independent clients vendor and replay the same contract.
func TestPublicGraphHashFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "contracts", "graph-snapshot-hash-v1")
	body, err := os.ReadFile(filepath.Join(dir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != manifest.Files["cases.json"] {
		t.Fatal("public fixture digest changed")
	}
	var fixtures struct{ Valid, Invalid []publicGraphHashCase }
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures.Valid {
		t.Run(fixture.Name, func(t *testing.T) {
			request, err := graphsnapshot.DecodeRequest(strings.NewReader(fixture.RequestJSON))
			if err != nil {
				t.Fatal(err)
			}
			var baseBefore []byte
			if fixture.Base != nil {
				baseBefore, _ = json.Marshal(fixture.Base)
			}
			graph, graphErr := materializePublicFixture(request, fixture.Base)
			if graphErr != nil {
				t.Fatal(graphErr)
			}
			canonical, hash, err := graphsnapshot.CanonicalHash(graph.Nodes, graph.Edges)
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != fixture.Canonical || hash != fixture.Hash || hash != request.ContentHash {
				t.Fatalf("fixture mismatch: canonical=%s hash=%s", canonical, hash)
			}
			if fixture.Base != nil {
				after, _ := json.Marshal(fixture.Base)
				if !reflect.DeepEqual(baseBefore, after) {
					t.Fatal("delta changed its base")
				}
			}
		})
	}
	for _, fixture := range fixtures.Invalid {
		t.Run(fixture.Name, func(t *testing.T) {
			request, err := graphsnapshot.DecodeRequest(strings.NewReader(fixture.RequestJSON))
			if fixture.Reject == "decode" {
				if err == nil {
					t.Fatal("invalid JSON accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			graph, graphErr := materializePublicFixture(request, fixture.Base)
			if fixture.Reject != "canonical" && fixture.Reject != "hash" {
				if graphErr == nil || string(graphErr.Code) != fixture.Reject {
					t.Fatalf("error=%v want=%s", graphErr, fixture.Reject)
				}
				return
			}
			if graphErr != nil {
				t.Fatal(graphErr)
			}
			_, hash, err := graphsnapshot.CanonicalHash(graph.Nodes, graph.Edges)
			if fixture.Reject == "canonical" {
				if err == nil {
					t.Fatal("uncanonicalizable input accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if hash == request.ContentHash {
				t.Fatal("incorrect hash accepted")
			}
		})
	}
}

func materializePublicFixture(request graphsnapshot.Request, base *graphsnapshot.Manifest) (graphsnapshot.Manifest, *graphsnapshot.Error) {
	if request.Mode == graphsnapshot.ModeFull {
		return graphsnapshot.NormalizeFull(request)
	}
	return graphsnapshot.ApplyDelta(*base, request)
}
