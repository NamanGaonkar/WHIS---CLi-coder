package tool

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Symbol is a named code entity with its byte/line span.
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // func | method | type | struct | interface
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Signature string `json:"signature,omitempty"`
	Receiver  string `json:"receiver,omitempty"`
}

// Index is an in-memory symbol table for the workspace.
type Index struct {
	syms   []Symbol
	byName map[string][]int
	byFile map[string][]int
}

// BuildIndex walks the workspace and indexes Go files precisely (go/ast) and
// other common languages via line-based heuristics. Skips vendor/hidden dirs.
func BuildIndex(root string) (*Index, error) {
	idx := &Index{byName: map[string][]int{}, byFile: map[string][]int{}}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "node_modules", ".venv", "venv", "vendor", "__pycache__", ".idea", ".vscode", "dist", "build":
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasSuffix(name, ".go"):
			idx.indexGo(path, rel)
		case strings.HasSuffix(name, ".py"):
			idx.indexRegex(path, rel, pyRe)
		case strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".js"):
			idx.indexRegex(path, rel, tsRe)
		case strings.HasSuffix(name, ".rs"):
			idx.indexRegex(path, rel, rsRe)
		}
		return nil
	})
	return idx, err
}

// --- Go: precise AST indexing ---

func (ix *Index) indexGo(abs, rel string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, nil, 0)
	if err != nil {
		return
	}
	add := func(name, kind string, start, end token.Pos, sig string) {
		ix.add(Symbol{
			Name: name, Kind: kind, File: rel,
			StartLine: fset.Position(start).Line,
			EndLine:   fset.Position(end).Line,
			Signature: sig,
		})
	}
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			kind := "func"
			sig := "func " + decl.Name.Name
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				kind = "method"
				recv := typesString(fset, decl.Recv.List[0].Type)
				sig = "func (" + recv + ") " + decl.Name.Name
				ix.add(Symbol{Name: decl.Name.Name, Kind: kind, File: rel, Receiver: recv,
					StartLine: fset.Position(decl.Pos()).Line, EndLine: fset.Position(decl.End()).Line, Signature: sig})
				continue
			}
			add(decl.Name.Name, kind, decl.Pos(), decl.End(), sig)
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					add(s.Name.Name, kind, s.Pos(), s.End(), "type "+s.Name.Name)
				}
			}
		}
	}
}

func typesString(fset *token.FileSet, expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + typesString(fset, t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typesString(fset, t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return typesString(fset, t.X) + "[" + typesString(fset, t.Index) + "]"
	case *ast.IndexListExpr:
		s := typesString(fset, t.X) + "["
		for i, arg := range t.Indices {
			if i > 0 {
				s += ", "
			}
			s += typesString(fset, arg)
		}
		return s + "]"
	default:
		return "?"
	}
}

// --- Other languages: line heuristics ---

var (
	pyRe = regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)|^\s*class\s+([A-Za-z_]\w*)`)
	tsRe = regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)|^\s*(?:export\s+)?class\s+([A-Za-z_$][\w$]*)|^\s*(?:export\s+)?(?:const|let)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?\(|^\s*(?:export\s+)?interface\s+([A-Za-z_$][\w$]*)`)
	rsRe = regexp.MustCompile(`^\s*(?:pub\s+)?fn\s+([A-Za-z_]\w*)|^\s*(?:pub\s+)?struct\s+([A-Za-z_]\w*)|^\s*(?:pub\s+)?(?:trait|enum)\s+([A-Za-z_]\w*)|^\s*impl(?:<[^>]*>)?\s+([A-Za-z_][\w:]*)`)
)

func (ix *Index) indexRegex(abs, rel string, re *regexp.Regexp) {
	b, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	for i, ln := range lines {
		m := re.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		name, kind := "", ""
		for g := 1; g < len(m); g++ {
			if m[g] != "" {
				name = m[g]
				break
			}
		}
		if name == "" {
			continue
		}
		kind = "def"
		if strings.Contains(m[0], "class") || strings.Contains(m[0], "struct") || strings.Contains(m[0], "interface") || strings.Contains(m[0], "trait") || strings.Contains(m[0], "enum") {
			kind = "type"
		}
		end := i + 1
		// naive block end: closing brace / dedent heuristic capped at 400 lines
		for j := i + 1; j < len(lines) && j < i+400; j++ {
			t := strings.TrimSpace(lines[j])
			if t == "}" || (kind == "type" && t != "" && !strings.HasPrefix(t, " ") && j > i+1) {
				end = j + 1
				break
			}
			end = j + 1
		}
		ix.add(Symbol{Name: name, Kind: kind, File: rel, StartLine: i + 1, EndLine: end, Signature: strings.TrimSpace(ln)})
	}
}

func (ix *Index) add(s Symbol) {
	i := len(ix.syms)
	ix.syms = append(ix.syms, s)
	ix.byName[s.Name] = append(ix.byName[s.Name], i)
	ix.byFile[s.File] = append(ix.byFile[s.File], i)
}

// EnsureIndex lazily builds the workspace index once per Env.
func (e *Env) EnsureIndex() (*Index, error) {
	if e.indexBuilt {
		return nil, nil
	}
	e.indexBuilt = true
	idx, err := BuildIndex(e.Root)
	if err != nil {
		return nil, err
	}
	e.index = nil // placeholder; we store on Env below
	return idx, nil
}

// LocateSymbol implements locate_symbol: return just the target symbol body.
func (e *Env) LocateSymbol(file, name string) Result {
	if e.index == nil {
		idx, err := BuildIndex(e.Root)
		if err != nil {
			return Result{Output: "index error: " + err.Error()}
		}
		e.index = idx
	}
	rel := filepath.ToSlash(file)
	var found []Symbol
	for _, s := range e.index.syms {
		if s.Name == name && (file == "" || s.File == rel || strings.HasSuffix(rel, s.File) || strings.HasSuffix(s.File, rel)) {
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		var near []string
		for _, s := range e.index.syms {
			if strings.Contains(strings.ToLower(s.Name), strings.ToLower(name)) {
				near = append(near, fmt.Sprintf("%s:%d %s %s", s.File, s.StartLine, s.Kind, s.Name))
			}
			if len(near) >= 10 {
				break
			}
		}
		out := fmt.Sprintf("symbol %q not found", name)
		if len(near) > 0 {
			out += "\nsimilar: " + strings.Join(near, " | ")
		}
		return Result{Output: out}
	}
	abs, err := e.resolve(found[0].File)
	if err != nil {
		return Result{Output: err.Error()}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Result{Output: err.Error()}
	}
	lines := strings.Split(string(b), "\n")
	var out strings.Builder
	for i, s := range found {
		if i > 0 {
			out.WriteString("\n---\n")
		}
		hi := s.EndLine
		if hi > len(lines) {
			hi = len(lines)
		}
		lo := s.StartLine - 1
		if lo < 0 {
			lo = 0
		}
		fmt.Fprintf(&out, "%s:%d-%d %s %s\n", s.File, s.StartLine, s.EndLine, s.Kind, s.Name)
		for ln := lo; ln < hi && ln < len(lines); ln++ {
			fmt.Fprintf(&out, "%4d| %s\n", ln+1, lines[ln])
		}
	}
	return Result{OK: true, Output: strings.TrimRight(out.String(), "\n")}
}
