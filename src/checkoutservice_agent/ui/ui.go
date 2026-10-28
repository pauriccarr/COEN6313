// Package ui serves the HTTP side of the agent on :8080: /health and the
// fault-injection endpoints the FaultLab dashboard drives
// (/ui, /ui/modes, /ui/run, /ui/status, /ui/results).
package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"checkoutservice-agent/agent"
	"checkoutservice-agent/faultlab"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

// Server runs the fault suite in the background and serves its state.
type Server struct {
	runner *faultlab.Runner
	log    *logrus.Logger
	dir    string

	mu      sync.Mutex
	status  map[string]any
	results *faultlab.Results
	logBuf  strings.Builder
}

// New serves the fault suite run by r (faultlab.Live or a mock Runner).
func New(r *faultlab.Runner, log *logrus.Logger) *Server {
	dir := os.Getenv("RESULTS_DIR")
	if dir == "" {
		dir = "/results"
	}
	s := &Server{runner: r, log: log, dir: dir,
		status: map[string]any{"running": false, "log": "", "backend": r.Backend}}
	r.Logf = s.logf
	// Results survive a restart if /results is a volume.
	if b, err := os.ReadFile(filepath.Join(dir, faultlab.ResultsFile)); err == nil {
		var res faultlab.Results
		if json.Unmarshal(b, &res) == nil {
			s.results = &res
		}
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ui/modes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"modes": agent.Modes, "checkpoints": agent.ExpectedCheckpoints})
	})
	mux.HandleFunc("POST /ui/run", s.run)
	mux.HandleFunc("GET /ui/status", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := map[string]any{}
		for k, v := range s.status {
			out[k] = v
		}
		out["log"] = s.logBuf.String()
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /ui/results", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.results == nil {
			writeJSON(w, map[string]any{"results": []any{}})
			return
		}
		writeJSON(w, s.results)
	})
	mux.HandleFunc("GET /ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	return mux
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Modes []string `json:"modes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	modes := faultlab.FilterModes(body.Modes)

	s.mu.Lock()
	if s.status["running"] == true {
		s.mu.Unlock()
		writeJSON(w, map[string]string{"status": "busy"})
		return
	}
	s.status = map[string]any{"running": true, "started_at": now(), "modes": modes, "backend": s.runner.Backend}
	s.logBuf.Reset()
	s.mu.Unlock()

	go s.runAll(modes)
	writeJSON(w, map[string]any{"status": "started", "modes": modes})
}

func (s *Server) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(&s.logBuf, format+"\n", args...)
}

func (s *Server) runAll(modes []string) {
	rc := 0
	defer func() {
		if p := recover(); p != nil {
			s.logf("PANIC: %v", p)
			rc = 2
		}
		s.mu.Lock()
		s.status["running"] = false
		s.status["finished_at"] = now()
		s.status["returncode"] = rc
		s.mu.Unlock()
	}()

	res := s.runner.RunAll(modes)
	for _, row := range res.Results {
		if row.Error != "" {
			rc = 1
		}
	}
	s.mu.Lock()
	s.results = &res
	s.mu.Unlock()
	if err := faultlab.Save(s.dir, res); err != nil {
		s.logf("could not save results to %s (kept in memory): %v", s.dir, err)
	}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

const page = `<!doctype html><html><head><meta charset="utf-8"><title>Checkout agent faults</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>body{font:14px system-ui;margin:16px;max-width:900px}pre{background:#f4f4f4;padding:8px;white-space:pre-wrap}
table{border-collapse:collapse}td,th{border:1px solid #ccc;padding:4px 8px;text-align:left}</style></head><body>
<h1>Checkout agent: fault injection</h1>
<div id="modes"></div><button id="run">Run selected</button> <span id="st"></span>
<pre id="log"></pre><div id="res"></div>
<script>
const $=s=>document.querySelector(s);
const esc=s=>String(s??"").replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]));
fetch("ui/modes").then(r=>r.json()).then(d=>{$("#modes").innerHTML=d.modes.map(m=>
 '<label><input type=checkbox value="'+m+'" checked> '+m+'</label> ').join("")});
$("#run").onclick=()=>fetch("ui/run",{method:"POST",headers:{"Content-Type":"application/json"},
 body:JSON.stringify({modes:[...document.querySelectorAll("#modes input:checked")].map(i=>i.value)})}).then(poll);
async function poll(){const s=await (await fetch("ui/status")).json();
 $("#st").textContent=s.running?"Running…":(s.finished_at?"Finished (exit "+s.returncode+")":"Idle");
 $("#log").textContent=s.log||"";const r=await (await fetch("ui/results")).json();
 $("#res").innerHTML="<p>Backend: "+esc(r.backend||"live")+"</p><table><tr><th>Mode</th><th>Last known well</th><th>Lost</th><th>Infection</th><th>Prop.</th><th>Caught</th><th>Outcome</th></tr>"+
 (r.results||[]).map(x=>"<tr><td>"+esc(x.fault_mode)+"</td><td>"+esc(x.last_known_well)+"</td><td>"+
 esc((x.steps_lost||[]).join(", "))+"</td><td title=\""+esc(x.infection_detail)+"\">"+esc(x.infection_point||"none")+"</td><td>"+
 esc(x.propagation_depth)+"</td><td>"+esc(x.detected_by||"-")+"</td><td>"+esc(x.error||x.outcome)+"</td></tr>").join("")+"</table>";
 if(s.running)setTimeout(poll,3000)}
poll();
</script></body></html>`
