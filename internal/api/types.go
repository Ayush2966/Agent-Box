// Package api is the daemon's versioned local HTTP API. The CLI and every UI are clients of it.
package api

import (
	"time"

	"github.com/Ayush2966/Agent-Box/internal/box"
	"github.com/Ayush2966/Agent-Box/internal/errs"
)

type RepoView struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type HealthView struct {
	OK      bool   `json:"ok"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
	APIAddr string `json:"api_addr"` // "" when the HTTP API is disabled
	UI      bool   `json:"ui"`       // whether a dashboard is built into this binary
}

type LoginCode struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type BoxView struct {
	ID        string            `json:"id"`
	Repo      string            `json:"repo"`
	RepoPath  string            `json:"repo_path"`
	Name      string            `json:"name"`
	Dir       string            `json:"dir"`
	Branch    string            `json:"branch"`
	State     string            `json:"state"`
	Ports     map[string]int    `json:"ports"`
	DBName    string            `json:"db_name,omitempty"`
	DBURL     string            `json:"db_url,omitempty"`
	PID       int               `json:"pid,omitempty"`
	LastStep  string            `json:"last_step,omitempty"`
	LastError string            `json:"last_error,omitempty"`
	Base      string            `json:"base,omitempty"`
	Ahead     int               `json:"ahead"`
	Behind    int               `json:"behind"`
	Dirty     int               `json:"dirty"`
	URL       string            `json:"url,omitempty"` // http://<name>.localhost:<port>/ when the box has ports
	Env       map[string]string `json:"env,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type Created struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
	Name string `json:"name"`
}

type MergeResult struct {
	Into string `json:"into"`
}

type ErrorBody struct {
	Error *errs.E `json:"error"`
}

func viewOf(i box.Info) BoxView {
	return BoxView{
		ID: i.ID(), Repo: i.RepoID, RepoPath: i.RepoPath, Name: i.Name, Dir: i.Dir, Branch: i.Branch,
		State: string(i.State), Ports: i.Ports, DBName: i.DBName, DBURL: i.DBURL, PID: i.PID,
		LastStep: i.LastStep, LastError: i.LastError, Base: i.Base, Ahead: i.Ahead, Behind: i.Behind,
		Dirty: i.Dirty, Env: i.Env, CreatedAt: i.CreatedAt,
	}
}

// OpenRequest picks what `POST .../open` launches on the box's directory.
type OpenRequest struct {
	App string `json:"app"` // editor (default) | terminal | files
}

type OpenResult struct {
	Launched string `json:"launched"` // the program or app that was started
}

type DiskUsage struct {
	Bytes int64 `json:"bytes"`
}

// PruneRequest removes stopped and failed boxes in Repo ("" = all repos).
type PruneRequest struct {
	Repo  string `json:"repo,omitempty"`
	Force bool   `json:"force,omitempty"`
}

type PruneResponse struct {
	Pruned []box.PruneResult `json:"pruned"`
}
