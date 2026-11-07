package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

type Principal struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	ClientID string `json:"clientId,omitempty"`
}
type principalKey struct{}

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}
func Identity(ctx context.Context) Principal { p, _ := ctx.Value(principalKey{}).(Principal); return p }
func (p Principal) Allows(scope string) bool {
	return p.Role == "admin" || scope == "read" && (p.Role == "viewer" || p.Role == "control") || scope == "control" && p.Role == "control"
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *APIError) Error() string                 { return e.Message }
func Fail(status int, code, message string) error { return &APIError{code, message, status} }

type Method struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Arguments []string `json:"arguments"`
	Mutation  bool     `json:"mutation"`
	fn        reflect.Value
}
type Registry struct {
	methods  map[string]Method
	sanitize func(error) error
}

func (r *Registry) SetErrorSanitizer(fn func(error) error) { r.sanitize = fn }

func NewRegistry() *Registry { return &Registry{methods: map[string]Method{}} }

var contextType = reflect.TypeFor[context.Context]()
var errorType = reflect.TypeFor[error]()

// Only explicitly registered, bound functions may be called. Reflection here
// decodes their Go argument types; it never resolves a method supplied by a client.
func (r *Registry) Register(name, scope string, mutation bool, fn any) {
	v := reflect.ValueOf(fn)
	if name == "" || !v.IsValid() || v.Kind() != reflect.Func || v.Type().IsVariadic() {
		panic("invalid remote method")
	}
	if _, ok := r.methods[name]; ok {
		panic("duplicate remote method: " + name)
	}
	if scope != "read" && scope != "control" && scope != "admin" {
		panic("invalid remote permission")
	}
	m := Method{Name: name, Scope: scope, Mutation: mutation, Arguments: []string{}, fn: v}
	for i := 0; i < v.Type().NumIn(); i++ {
		if v.Type().In(i) != contextType {
			m.Arguments = append(m.Arguments, v.Type().In(i).String())
		}
	}
	r.methods[name] = m
}
func (r *Registry) Catalog(p Principal) []Method {
	out := []Method{}
	for _, m := range r.methods {
		if p.Allows(m.Scope) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (r *Registry) Lookup(name string) (Method, bool) { m, ok := r.methods[name]; return m, ok }
func (r *Registry) Call(ctx context.Context, name string, body json.RawMessage) (result any, err error) {
	defer func() {
		if r.sanitize != nil && err != nil {
			err = r.sanitize(err)
		}
	}()
	m, ok := r.methods[name]
	if !ok {
		return nil, Fail(404, "unknown_method", "Method is not available")
	}
	if !Identity(ctx).Allows(m.Scope) {
		return nil, Fail(403, "forbidden", "This device is not allowed to perform this operation")
	}
	var raw []json.RawMessage
	if len(body) != 0 && string(body) != "null" {
		if err = json.Unmarshal(body, &raw); err != nil {
			return nil, Fail(400, "invalid_arguments", "Arguments must be a JSON array")
		}
	}
	if len(raw) != len(m.Arguments) {
		return nil, Fail(400, "invalid_arguments", fmt.Sprintf("Expected %d arguments", len(m.Arguments)))
	}
	values := []reflect.Value{}
	index := 0
	for i := 0; i < m.fn.Type().NumIn(); i++ {
		typ := m.fn.Type().In(i)
		if typ == contextType {
			values = append(values, reflect.ValueOf(ctx))
			continue
		}
		value := reflect.New(typ)
		d := json.NewDecoder(strings.NewReader(string(raw[index])))
		d.DisallowUnknownFields()
		if e := d.Decode(value.Interface()); e != nil {
			return nil, Fail(400, "invalid_arguments", fmt.Sprintf("Invalid argument %d", index+1))
		}
		if e := d.Decode(new(any)); e != io.EOF {
			return nil, Fail(400, "invalid_arguments", "Trailing argument data")
		}
		values = append(values, value.Elem())
		index++
	}
	defer func() {
		if recover() != nil {
			result = nil
			err = errors.New("backend operation failed")
		}
	}()
	out := m.fn.Call(values)
	if len(out) > 0 && out[len(out)-1].Type().Implements(errorType) {
		last := out[len(out)-1]
		out = out[:len(out)-1]
		if !last.IsNil() {
			return nil, last.Interface().(error)
		}
	}
	if len(out) == 0 {
		return true, nil
	}
	if len(out) == 1 {
		return out[0].Interface(), nil
	}
	list := []any{}
	for _, v := range out {
		list = append(list, v.Interface())
	}
	return list, nil
}
