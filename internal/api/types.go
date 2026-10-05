// Package api is the daemon's versioned local HTTP API. The CLI and every UI are clients of it.
package api

import (
	"time"

	"github.com/agentbox-dev/agentbox/internal/box"
	"github.com/agentbox-dev/agentbox/internal/errs"
)

type RepoView struct {
	ID   string `json:"id"`
	Path string `json:"path"`
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
