package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"time"
)

type Operation struct {
	ID              string          `json:"id"`
	Owner           string          `json:"owner"`
	Method          string          `json:"method"`
	Fingerprint     string          `json:"fingerprint"`
	Status          string          `json:"status"`
	StartedAt       time.Time       `json:"startedAt"`
	FinishedAt      time.Time       `json:"finishedAt,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           string          `json:"error,omitempty"`
	ResultTruncated bool            `json:"resultTruncated,omitempty"`
}
type Operations struct {
	mu      sync.Mutex
	records []Operation
	active  int
	state   State
	call    Call
	ctx     context.Context
}

var operationID = regexp.MustCompile(`^[0-9]{13}_[A-Za-z0-9_-]{8,64}$`)

func NewOperations(ctx context.Context, state State, call Call) (*Operations, error) {
	o := &Operations{state: state, ctx: ctx, call: call}
	if state != nil {
		raw, err := state.Load(ctx, "operations")
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &o.records); err != nil {
				return nil, err
			}
		}
	}
	changed := false
	for i := range o.records {
		if o.records[i].Status == "running" {
			o.records[i].Status = "interrupted"
			o.records[i].Error = "Server restarted; inspect retained session changes before submitting a new operation"
			changed = true
		}
	}
	if changed {
		if err := o.save(); err != nil {
			return nil, err
		}
	}
	return o, nil
}
func (o *Operations) save() error {
	if o.state == nil {
		return nil
	}
	raw, err := json.Marshal(o.records)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return o.state.Save(ctx, "operations", raw)
}
func (o *Operations) Submit(p Principal, id, method string, params json.RawMessage) (Operation, error) {
	if !operationID.MatchString(id) {
		return Operation{}, Fail(400, "invalid_operation_id", "Use <server Unix milliseconds>_<random request ID>")
	}
	var decoded any
	if len(params) > 0 {
		if err := json.Unmarshal(params, &decoded); err != nil {
			return Operation{}, Fail(400, "invalid_arguments", "Invalid JSON arguments")
		}
	}
	canonical, _ := json.Marshal(decoded)
	sum := sha256.Sum256(append([]byte(method+"\n"), canonical...))
	fingerprint := hex.EncodeToString(sum[:])
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, op := range o.records {
		if op.Owner == p.ID && op.ID == id {
			if op.Fingerprint != fingerprint {
				return Operation{}, Fail(409, "operation_conflict", "This operation ID already identifies a different request")
			}
			return op, nil
		}
	}
	issued, _ := strconv.ParseInt(id[:13], 10, 64)
	age := time.Since(time.UnixMilli(issued))
	if age > 5*time.Minute || age < -2*time.Minute {
		return Operation{}, Fail(410, "operation_expired", "Request ID expired or server time differs; inspect existing operations before creating a new request")
	}
	if o.active >= 8 {
		return Operation{}, Fail(429, "operation_limit", "Too many operations are running")
	}
	if len(o.records) >= 512 {
		found := -1
		for i, op := range o.records {
			if op.Status != "running" && time.Since(op.StartedAt) > 10*time.Minute {
				found = i
				break
			}
		}
		if found < 0 {
			return Operation{}, Fail(429, "operation_limit", "Operation history is full")
		}
		o.records = append(o.records[:found], o.records[found+1:]...)
	}
	op := Operation{ID: id, Owner: p.ID, Method: method, Fingerprint: fingerprint, Status: "running", StartedAt: time.Now().UTC()}
	o.records = append(o.records, op)
	if err := o.save(); err != nil {
		o.records = o.records[:len(o.records)-1]
		return Operation{}, err
	}
	o.active++
	// The execution belongs to the server, not the requesting HTTP connection.
	go func() {
		result, err := o.call(WithPrincipal(o.ctx, p), method, params)
		raw, encodeErr := json.Marshal(result)
		if err == nil {
			err = encodeErr
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		o.active--
		for i := range o.records {
			item := &o.records[i]
			if item.ID == id && item.Owner == p.ID {
				item.FinishedAt = time.Now().UTC()
				item.Status = "completed"
				if err != nil {
					item.Status = "failed"
					item.Error = "Operation failed; inspect the associated session or run"
					var safe *APIError
					if errors.As(err, &safe) {
						item.Error = safe.Message
					}
				} else if len(raw) > 16<<20 {
					item.ResultTruncated = true
					item.Error = "Operation completed; inspect its session or artifact for the full result"
				} else {
					item.Result = raw
				}
				_ = o.save()
				return
			}
		}
	}()
	return op, nil
}
func (o *Operations) Get(p Principal, id string) (Operation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, op := range o.records {
		if op.ID == id && (op.Owner == p.ID || p.Role == "admin") {
			return op, nil
		}
	}
	return Operation{}, Fail(404, "operation_not_found", "Operation not found")
}
func (o *Operations) List(p Principal) []Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := []Operation{}
	for _, op := range o.records {
		if op.Owner == p.ID || p.Role == "admin" {
			out = append(out, op)
		}
	}
	return out
}
