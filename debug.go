package main

import (
	"context"
	"encoding/json"
	"fmt"

	"nono/internal/claude"
	"nono/internal/dsh"
	"nono/internal/hub"
	"nono/internal/opencode"
	"nono/internal/workbuddy"
	"nono/internal/zcode"
)

// debugAgents prints recent sessions of the adapters that read other apps'
// stores, without requiring the apps to be running. It backs `nono agents`.
func debugAgents() {
	sources := []hub.Source{
		opencode.New(),
		claude.New(),
		zcode.New(),
		workbuddy.New(),
		dsh.New(),
	}
	type brief struct {
		ID      string `json:"id"`
		Project string `json:"project"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Detail  string `json:"detail"`
		Updated int64  `json:"updated"`
		TTFT    int64  `json:"ttftMs"`
		Post    int64  `json:"postFirstMs"`
		TPS     string `json:"tps"`
		Tokens  int    `json:"outputTokens"`
		Recent  int    `json:"recentTurns"`
	}
	ctx := context.Background()
	for _, source := range sources {
		sessions, err := source.Sessions(ctx)
		out := struct {
			Agent    string  `json:"agent"`
			Running  bool    `json:"running"`
			Error    string  `json:"error,omitempty"`
			Sessions []brief `json:"sessions"`
		}{Agent: source.ID(), Running: source.Running()}
		if err != nil {
			out.Error = err.Error()
		}
		for _, session := range sessions {
			tokens := 0
			if len(session.Metrics.Recent) > 0 {
				tokens = session.Metrics.Recent[0].OutputTokens
			}
			out.Sessions = append(out.Sessions, brief{
				ID:      session.ID,
				Project: session.Project,
				Title:   session.Title,
				State:   string(session.State),
				Detail:  session.Detail,
				Updated: session.LastActivity.UnixMilli(),
				TTFT:    session.Metrics.AvgTTFT.Milliseconds(),
				Post:    session.Metrics.AvgPostFirst.Milliseconds(),
				TPS:     fmt.Sprintf("%.1f", session.Metrics.AvgTPS),
				Tokens:  tokens,
				Recent:  len(session.Metrics.Recent),
			})
		}
		data, _ := json.Marshal(out)
		fmt.Println(string(data))
	}
}
