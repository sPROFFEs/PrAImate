package assistant

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Discovery is deterministic and never executes actions. Normalize common UI
// vocabulary so Spanish requests need no model-generated English translation.
var actionVocabulary = map[string]string{}

func init() {
	for _, group := range []string{
		"agent agents agente agentes persona personas",
		"chat chats conversation conversations conversacion conversaciones",
		"worker workers trabajador trabajadores",
		"model models modelo modelos",
		"settings setting ajustes ajuste configuracion preferencias",
		"theme appearance tema temas apariencia oscuro clara claro dark light",
		"skill skills habilidad habilidades",
		"document documents documento documentos",
		"code codigo",
		"terminal terminals terminales",
		"create crear crea creame crearme nuevo nueva new anadir agrega agregar",
		"change set configure configurar configura cambie cambia cambiar modifica modificar",
		"list listar lista listar mostrar muestra ver show",
		"read inspect leer lee consultar consulta inspeccionar revisa revisar",
		"search find buscar busca encuentra encontrar",
		"navigate open start abrir abre abreme iniciar inicia",
		"delete remove borrar borra eliminar elimina",
		"rename renombrar renombra nombre title titulo",
		"clone clonar clona duplicar duplica copy copiar copia",
		"instructions instruccion instrucciones prompt prompts",
		"fast rapido rapida pequeno pequena simple sencillo",
		"middle medio intermedio mediano",
		"primary reasoner razonador principal razonamiento",
		"enable enabled habilitar habilita activar activa activado",
		"disable disabled deshabilitar deshabilita desactivar desactiva desactivado",
		"command commands comando comandos ejecutar ejecuta execute run",
		"task tasks tarea tareas",
		"network red",
	} {
		terms := strings.Fields(group)
		for _, term := range terms {
			actionVocabulary[term] = terms[0]
		}
	}
}

func actionTerms(text string) map[string]bool {
	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, norm.NFD.String(text))
	terms := map[string]bool{}
	for _, term := range strings.Fields(text) {
		switch term {
		case "a", "an", "the", "to", "my", "me", "please", "of", "in", "on", "for", "with", "and", "is", "it", "un", "una", "unos", "unas", "el", "la", "los", "las", "de", "del", "al", "en", "mi", "mis", "por", "favor", "que", "quiero", "puedes", "puede", "con", "y", "para", "praimate":
			continue
		}
		if canonical, ok := actionVocabulary[term]; ok {
			term = canonical
		}
		terms[term] = true
	}
	return terms
}

func (r *Registry) Search(query string, limit int) []Action {
	if limit < 1 || limit > 6 {
		limit = 6
	}
	type match struct {
		a     Action
		score int
	}
	terms := actionTerms(query)
	domains := map[string]bool{}
	for _, domain := range []string{"agent", "chat", "worker", "skill", "mcp", "code", "terminal", "task"} {
		if terms[domain] {
			domains[domain] = true
		}
	}
	found := []match{}
	for _, a := range r.actions {
		// The catalogue helpers mention every feature. Treat them as fallbacks,
		// otherwise they crowd out the operation the user actually requested.
		if strings.TrimSpace(query) != "" && (a.Name == "actions.search" || a.Name == "app.search" && !terms["search"]) {
			continue
		}
		if a.Name == "code.start" && terms["navigate"] && !terms["terminal"] && !terms["create"] && !terms["session"] && !terms["cli"] && strings.TrimSpace(query) != "code.start" {
			continue
		}
		name := actionTerms(a.Name)
		description := a.Description
		for _, field := range a.Fields {
			description += " " + field.Description
			description += " " + strings.Join(field.Enum, " ")
		}
		detail := actionTerms(description)
		if len(domains) > 0 {
			matchesDomain := false
			for domain := range domains {
				matchesDomain = matchesDomain || name[domain]
				if a.Name == "ui.navigate" && terms["navigate"] || a.Name == "app.search" && terms["search"] {
					matchesDomain = matchesDomain || detail[domain]
				}
			}
			// Code is the domain name for terminal session actions.
			matchesDomain = matchesDomain || domains["terminal"] && name["code"]
			if !matchesDomain {
				continue
			}
		}
		score := 0
		for term := range terms {
			if name[term] {
				switch term {
				case "create", "change", "list", "read", "search", "navigate", "delete", "rename", "clone", "enable", "disable":
					score += 6
				default:
					score += 4
				}
			} else if detail[term] {
				score++
			}
		}
		if score > 0 || strings.TrimSpace(query) == "" {
			found = append(found, match{a, score})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].score == found[j].score {
			return found[i].a.Name < found[j].a.Name
		}
		return found[i].score > found[j].score
	})
	out := []Action{}
	for _, m := range found {
		if len(out) >= limit {
			break
		}
		out = append(out, m.a)
	}
	return out
}
