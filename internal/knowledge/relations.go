package knowledge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var markdownLink = regexp.MustCompile(`\[[^\]\n]*\]\(([^)\s]+)`)

// connectSources adds local document links, Go imports and unambiguous calls
// between files in the same package. Other languages remain text searchable.
func connectSources(idx *Index, contents map[string][]byte) {
	known := map[string]bool{}
	functions := map[string]string{}
	parsed := map[string]*ast.File{}
	ids := map[string]bool{}
	for _, n := range idx.Graph.Nodes {
		ids[n.ID] = true
	}
	for _, doc := range idx.Documents {
		known[doc.Path] = true
		if !strings.HasSuffix(doc.Path, ".go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), doc.Path, contents[doc.Path], 0)
		if err != nil {
			continue
		}
		parsed[doc.Path] = file
		for _, decl := range file.Decls {
			if f, ok := decl.(*ast.FuncDecl); ok && f.Recv == nil {
				key := path.Dir(doc.Path) + "#" + file.Name.Name + "/" + f.Name.Name
				if _, exists := functions[key]; exists {
					functions[key] = ""
				} else {
					functions[key] = "go:" + doc.Path + ":" + f.Name.Name
				}
			}
		}
	}
	for _, doc := range idx.Documents {
		source := "file:" + doc.Path
		if file := parsed[doc.Path]; file != nil {
			for _, spec := range file.Imports {
				name, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					continue
				}
				id := "package:" + name
				if !ids[id] {
					idx.Graph.Nodes = append(idx.Graph.Nodes, Node{id, name, "code", doc.Path, ""})
					ids[id] = true
				}
				idx.Graph.Links = append(idx.Graph.Links, Edge{source, id, "imports", "EXTRACTED", 1, doc.Path})
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				name := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					name = receiverName(fn.Recv.List[0].Type) + "." + name
				}
				caller := "go:" + doc.Path + ":" + name
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					ident, ok := call.Fun.(*ast.Ident)
					if !ok || ident.Obj != nil {
						return true
					}
					target := functions[path.Dir(doc.Path)+"#"+file.Name.Name+"/"+ident.Name]
					if target != "" {
						idx.Graph.Links = append(idx.Graph.Links, Edge{caller, target, "calls", "EXTRACTED", 1, doc.Path})
					}
					return true
				})
			}
		}
		if ext := strings.ToLower(path.Ext(doc.Path)); ext == ".md" || ext == ".markdown" {
			for _, match := range markdownLink.FindAllStringSubmatch(string(contents[doc.Path]), -1) {
				u, err := url.Parse(match[1])
				if err != nil || u.IsAbs() || u.Host != "" || u.Path == "" {
					continue
				}
				target := path.Clean(path.Join(path.Dir(doc.Path), u.Path))
				if known[target] {
					idx.Graph.Links = append(idx.Graph.Links, Edge{source, "file:" + target, "references", "EXTRACTED", 1, doc.Path})
				}
			}
		}
	}
	// A simple NetworkX graph stores one edge per source/target/relation.
	seen := map[string]bool{}
	links := []Edge{}
	for _, edge := range idx.Graph.Links {
		key := edge.Source + "\x00" + edge.Target + "\x00" + edge.Relation
		if !seen[key] {
			seen[key] = true
			links = append(links, edge)
		}
	}
	idx.Graph.Links = links
}
