package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/assignments"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/db"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/httpapi"
	"wanxiang-agent/server/internal/issues"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/planning"
	"wanxiang-agent/server/internal/tasks"
)

type App struct {
	Config      config.Config
	DB          *sql.DB
	Launcher    *agents.Launcher
	Planning    *planning.Worker
	Assignments *assignments.Worker
	HTTP        httpapi.Dependencies
}

func New(cfg config.Config) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	conn, err := db.Open(filepath.Join(cfg.DataDir, "app.db"))
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(context.Background(), conn); err != nil {
		conn.Close()
		return nil, err
	}
	bus := events.NewBus(conn)
	agentSvc := agents.NewService(cfg, conn, bus)
	launcher := agents.NewLauncher(agentSvc, bus)
	if _, err := launcher.StartAll(context.Background()); err != nil {
		conn.Close()
		return nil, err
	}
	issueSvc := issues.NewService(conn, bus)
	taskSvc := tasks.NewService(cfg, conn, bus)
	mrSvc := mr.NewService(cfg, conn, bus, agentSvc, issueSvc)
	planningSvc := planning.NewService(cfg, conn, agentSvc)
	planningWorker := planning.NewWorker(conn, planningSvc, agentSvc, 2*time.Second)
	planningWorker.Start()
	assignmentSvc := assignments.NewService(cfg, conn)
	assignmentWorker := assignments.NewWorker(conn, assignmentSvc, 2*time.Second)
	assignmentWorker.Start()
	return &App{
		Config:      cfg,
		DB:          conn,
		Launcher:    launcher,
		Planning:    planningWorker,
		Assignments: assignmentWorker,
		HTTP:        httpapi.Dependencies{DB: conn, Agents: agentSvc, Launcher: launcher, Bus: bus, Tasks: taskSvc, MR: mrSvc, Issues: issueSvc, Assignments: assignmentSvc},
	}, nil
}

func (a *App) Close() error {
	if a.Assignments != nil {
		a.Assignments.Close()
	}
	if a.Planning != nil {
		a.Planning.Close()
	}
	if a.Launcher != nil {
		a.Launcher.Close()
	}
	if a.DB != nil {
		return a.DB.Close()
	}
	return nil
}
