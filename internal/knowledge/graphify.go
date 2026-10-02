package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ReadGraphify accepts both NetworkX variants (links/edges) and extraction JSON.
// It never modifies the original graph. Unsupported attributes are not executed.
func ReadGraphify(dir string) (Graph, string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Graph{}, "", err
	}
	defer root.Close()
	f, err := openRegular(root, "graphify-out/graph.json", maxIndex)
	if err != nil {
		return Graph{}, "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxIndex+1))
	if err != nil {
		return Graph{}, "", err
	}
	if len(raw) > maxIndex {
		return Graph{}, "", fmt.Errorf("Graphify graph exceeds 96 MiB")
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var wire struct {
		Directed bool             `json:"directed"`
		Nodes    []map[string]any `json:"nodes"`
		Links    []Edge           `json:"links"`
		Edges    []Edge           `json:"edges"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil {
		return Graph{}, digest, err
	}
	g := Graph{Directed: wire.Directed, Metadata: map[string]any{"engine": "graphify"}, Links: wire.Links, Nodes: []Node{}}
	if g.Links == nil {
		g.Links = wire.Edges
	}
	for _, n := range wire.Nodes {
		str := func(key string) string {
			v := n[key]
			if v == nil {
				return ""
			}
			return fmt.Sprint(v)
		}
		g.Nodes = append(g.Nodes, Node{str("id"), str("label"), str("file_type"), str("source_file"), str("source_location")})
	}
	return g, digest, nil
}

func HasGraphifyIndex(dir string) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Stat("graphify-out/graph.json")
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= maxIndex
}

func addGraphify(idx *Index, dir string) {
	g, digest, err := ReadGraphify(dir)
	idx.GraphifySHA256 = digest
	if err != nil {
		if !os.IsNotExist(err) {
			idx.Skipped = append(idx.Skipped, "Graphify graph: "+err.Error())
		}
		return
	}
	known := map[string]bool{}
	for _, d := range idx.Documents {
		known[d.Path] = true
	}
	ids := map[string]bool{}
	for _, n := range idx.Graph.Nodes {
		ids[n.ID] = true
	}
	imported := map[string]bool{}
	sources := map[string]string{}
	for _, n := range g.Nodes {
		if n.ID == "" || !known[n.SourceFile] || ids[n.ID] {
			continue
		}
		idx.Graph.Nodes = append(idx.Graph.Nodes, n)
		imported[n.ID] = true
		sources[n.ID] = n.SourceFile
		ids[n.ID] = true
	}
	for _, e := range g.Links {
		if e.SourceFile == "" {
			e.SourceFile = sources[e.Source]
		}
		if imported[e.Source] && imported[e.Target] && known[e.SourceFile] {
			idx.Graph.Links = append(idx.Graph.Links, e)
		}
	}
}
