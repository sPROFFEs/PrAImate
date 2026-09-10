package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrAgentNotFound is returned by GetAgent/DeleteAgent when no agent
// matches the supplied id. Callers should check with errors.Is.
var ErrAgentNotFound = errors.New("agent not found")

// ImportAgent reads an agent YAML file from disk, validates it, and
// upserts it into the DB. The agent's `id` is the primary key; an
// import with an existing id is treated as an update.
//
// Returns the freshly-stored Agent (including auto-set timestamps).
func (c *Core) ImportAgent(ctx context.Context, path string) (*Agent, error) {
	if c.store == nil {
		return nil, errors.New("ImportAgent: no store configured")
	}
	a, err := LoadAgentFile(path)
	if err != nil {
		return nil, err
	}
	return c.upsertAgent(ctx, a)
}

// ImportAgentYAML mirrors ImportAgent for cases where the YAML body is
// already in memory (e.g. embedded built-ins, HTTP POST from the GUI).
func (c *Core) ImportAgentYAML(ctx context.Context, body []byte, sourcePath string) (*Agent, error) {
	if c.store == nil {
		return nil, errors.New("ImportAgentYAML: no store configured")
	}
	a, err := ParseAgentYAML(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a.SourcePath = sourcePath
	return c.upsertAgent(ctx, a)
}

// ExportAgent writes the named agent back out as YAML at path. The
// file is created with 0o644; the caller is responsible for the
// destination directory existing.
func (c *Core) ExportAgent(ctx context.Context, id, path string) error {
	a, err := c.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	body, err := MarshalAgentYAML(a)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// GetAgent fetches one agent by id. Returns ErrAgentNotFound if it
// does not exist.
func (c *Core) GetAgent(ctx context.Context, id string) (*Agent, error) {
	if c.store == nil {
		return nil, errors.New("GetAgent: no store configured")
	}
	row := c.store.DB().QueryRowContext(ctx, agentSelectByID, id)
	a, err := scanAgent(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	return a, err
}

// DeleteAgent removes one agent from the DB. Returns ErrAgentNotFound
// if no row matched.
func (c *Core) DeleteAgent(ctx context.Context, id string) error {
	if c.store == nil {
		return errors.New("DeleteAgent: no store configured")
	}
	res, err := c.store.DB().ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	return nil
}

// listAgentsFromDB is the private query backing ListAgents in core.go.
func (c *Core) listAgentsFromDB(ctx context.Context) ([]Agent, error) {
	rows, err := c.store.DB().QueryContext(ctx, agentSelectAll)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (c *Core) upsertAgent(ctx context.Context, a *Agent) (*Agent, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if err := c.retainSkillSelections(ctx, "agent:"+a.ID, agentSkillScopes(a)); err != nil {
		return nil, err
	}
	tools, _ := json.Marshal(orEmpty(a.Tools))
	mcps, _ := json.Marshal(orEmpty(a.MCPServers))
	supports, _ := json.Marshal(orEmpty(a.Supports))
	surfaces, _ := json.Marshal(orEmpty(a.Surfaces))
	requirements, _ := json.Marshal(a.Requirements)
	skillConfig, err := json.Marshal(a.Skills)
	if err != nil {
		return nil, err
	}
	skillLock, err := json.Marshal(a.SkillsLock)
	if err != nil {
		return nil, err
	}
	wfs, err := json.Marshal(orEmptyWorkflows(a.Workflows))
	if err != nil {
		return nil, fmt.Errorf("marshal workflows: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = c.store.DB().ExecContext(ctx, agentUpsert,
		a.ID, a.Name, a.Description, nullableText(a.Icon),
		a.Instructions, string(tools), string(mcps), string(wfs), string(supports),
		string(surfaces), a.Knowledge, string(requirements), a.DefaultWorkflow, nullableText(a.SourcePath), now, now,
		a.Schema, string(skillConfig), string(skillLock),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert agent %s: %w", a.ID, err)
	}
	return c.GetAgent(ctx, a.ID)
}

func scanAgent(scan func(...any) error) (*Agent, error) {
	var (
		a                                                                          Agent
		toolsJSON, mcpsJSON, wfsJSON, supportsJSON, surfacesJSON, requirementsJSON string
		icon, sourcePath                                                           sql.NullString
		createdAt, updatedAt                                                       string
		skillConfigJSON, skillLockJSON                                             string
	)
	_ = createdAt
	_ = updatedAt
	err := scan(
		&a.ID, &a.Name, &a.Description, &icon,
		&a.Instructions, &toolsJSON, &mcpsJSON, &wfsJSON, &supportsJSON,
		&surfacesJSON, &a.Knowledge, &requirementsJSON, &a.DefaultWorkflow, &sourcePath, &createdAt, &updatedAt,
		&a.Schema, &skillConfigJSON, &skillLockJSON,
	)
	if err != nil {
		return nil, err
	}
	if icon.Valid {
		a.Icon = icon.String
	}
	if sourcePath.Valid {
		a.SourcePath = sourcePath.String
	}
	if err := json.Unmarshal([]byte(toolsJSON), &a.Tools); err != nil {
		return nil, fmt.Errorf("decode tools_json: %w", err)
	}
	if err := json.Unmarshal([]byte(mcpsJSON), &a.MCPServers); err != nil {
		return nil, fmt.Errorf("decode mcp_servers_json: %w", err)
	}
	if err := json.Unmarshal([]byte(supportsJSON), &a.Supports); err != nil {
		return nil, fmt.Errorf("decode supports_json: %w", err)
	}
	if err := json.Unmarshal([]byte(wfsJSON), &a.Workflows); err != nil {
		return nil, fmt.Errorf("decode workflows_json: %w", err)
	}
	if err := json.Unmarshal([]byte(surfacesJSON), &a.Surfaces); err != nil {
		return nil, fmt.Errorf("decode surfaces_json: %w", err)
	}
	if requirementsJSON != "" && requirementsJSON != "null" && requirementsJSON != "{}" {
		if err := json.Unmarshal([]byte(requirementsJSON), &a.Requirements); err != nil {
			return nil, fmt.Errorf("decode requirements_json: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(skillConfigJSON), &a.Skills); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(skillLockJSON), &a.SkillsLock); err != nil {
		return nil, err
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return &a, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyWorkflows(w []Workflow) []Workflow {
	if w == nil {
		return []Workflow{}
	}
	return w
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const (
	agentColumns = `id, name, description, icon, instructions,
		tools_json, mcp_servers_json, workflows_json, supports_json,
		surfaces_json, knowledge, requirements_json, default_workflow, source_path, created_at, updated_at,
		agent_schema, skill_config_json, skill_lock_json`

	agentSelectAll  = `SELECT ` + agentColumns + ` FROM agents ORDER BY name`
	agentSelectByID = `SELECT ` + agentColumns + ` FROM agents WHERE id = ?`

	agentUpsert = `INSERT INTO agents (
		id, name, description, icon, instructions,
		tools_json, mcp_servers_json, workflows_json, supports_json,
		surfaces_json, knowledge, requirements_json, default_workflow, source_path, created_at, updated_at,
		agent_schema, skill_config_json, skill_lock_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name             = excluded.name,
		description      = excluded.description,
		icon             = excluded.icon,
		instructions     = excluded.instructions,
		tools_json       = excluded.tools_json,
		mcp_servers_json = excluded.mcp_servers_json,
		workflows_json   = excluded.workflows_json,
		supports_json    = excluded.supports_json,
		surfaces_json    = excluded.surfaces_json,
		knowledge        = excluded.knowledge,
		requirements_json = excluded.requirements_json,
		default_workflow = excluded.default_workflow,
		source_path      = excluded.source_path,
		updated_at       = excluded.updated_at,
		agent_schema = excluded.agent_schema,
		skill_config_json = excluded.skill_config_json,
		skill_lock_json = excluded.skill_lock_json`
)
