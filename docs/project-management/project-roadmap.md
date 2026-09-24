# COEN6313 PROJECT — Google Online Boutique

## 1. PROJECT PHASES & TASK OWNERSHIP

### PHASE 1 — BASELINE & CLOUD SETUP

- **Google Cloud Environment**
  - Create / configure GCP project
  - Configure GKE environment
  - Configure local access to GCP / GKE
- **Baseline Deployment**
  - Deploy the original Online Boutique
  - Verify Kubernetes workloads
  - Verify the original application is working
- **Baseline Verification**
  - Verify original microservices are running
  - Confirm the baseline system before modification

### PHASE 2 — INFRASTRUCTURE & PROJECT SETUP

- **CI/CD**
  - Configure GitHub Actions workflows
  - Deploy workflow
  - Undeploy workflow
  - Verify deployment automation
  - **Owner:** pauriccarr
- **NoSQL Migration**
  - Review existing Cart Service storage
  - Migrate Redis → MongoDB
  - Integrate MongoDB with Cart Service
  - Verify cart data operations
  - **Owner:** pauriccarr
- **GitHub / GCP Integration**
  - Configure GitHub → GCP authentication
  - Configure GKE access from GitHub Actions
  - Verify GitHub Actions can access the GKE cluster
  - **Owner:** Shared

### PHASE 3 — AGENTIFICATION

- **Recommendation Service v1 Analysis**
  - Understand v1 input / output
  - Understand existing recommendation logic
  - Identify limitations and opportunities for agentification
  - **Owner:** jingyawang
- **Agent v2 Design**
  - Define Agent v2 capabilities
  - Design Agent v2 workflow
  - Define model-driven decision logic
  - Preserve the required service API/interface
  - **Owner:** jingyawang
- **Model / Runtime / Tool Selection**
  - Select LLM / model
  - Select model runtime / API
  - Define required tools / backend calls
  - **Owner:** jingyawang
- **Agent v2 Implementation**
  - Implement agent logic
  - Integrate model access
  - Integrate required tools / backend services
  - Integrate Agent v2 with Online Boutique
  - **Owner:** jingyawang
- **Agent v2 Testing**
  - Verify input / output behavior
  - Verify compatibility with the existing API
  - Test basic agent behavior
  - Prepare Agent v2 for deployment
  - **Owner:** jingyawang

### PHASE 4 — CANARY INTEGRATION

- **v1 / v2 Deployment**
  - Deploy Recommendation Service v1
  - Deploy Recommendation Agent v2
  - Verify v1 and v2 can run simultaneously
  - **Owner:** shared
- **Canary Routing**
  - Configure traffic routing between v1 and v2
  - Verify requests can reach both versions
  - **Owner:** shared
- **Traffic Splitting**
  - Define canary traffic stages
  - Run controlled traffic splits
  - Support gradual migration toward v2
  - **Owner:** shared
- **Promotion / Rollback**
  - Define promotion conditions
  - Define rollback conditions
  - Verify promotion mechanism
  - Verify rollback mechanism
  - **Owner:** shared

### PHASE 5 — OBSERVABILITY & MONITORING

- **Instrumentation / Telemetry**
  - Identify required telemetry
  - Instrument relevant services / agent
  - Configure traces / metrics as needed
  - OpenTelemetry integration
  - **Owner:** pauriccarr
- **Metrics Collection**
  - Collect application / service metrics
  - Collect v1 / v2 metrics
  - Configure Prometheus
  - Verify metrics are available for monitoring
  - **Owner:** TBD
- **Google Cloud Monitoring**
  - Configure required monitoring resources
  - Connect relevant metrics
  - Verify monitoring data availability
  - **Owner:** TBD
- **Dashboard & Widgets**
  - Create monitoring dashboard
  - Availability / Error Rate widget
  - p95 Latency widget
  - Request / Traffic widget
  - Cost widget
  - Additional widgets if required
  - **Owner:** TBD
- **Cost Monitoring**
  - Configure cloud budget / cost monitoring
  - Monitor infrastructure cost
  - Monitor Agent / model usage cost
  - Compare v1 and v2 cost
  - Calculate / analyze cost per request if applicable
  - **Owner:** TBD

### PHASE 6 — EXPERIMENT, EVALUATION & DELIVERY

- **Canary Experiment**
  - Define experiment workload
  - Run traffic stages
  - Keep workload conditions consistent
  - Collect experimental data
  - **Owner:** TBD
- **v1 vs v2 Evaluation**
  - Compare Availability / Error Rate
  - Compare p95 Latency
  - Compare Correctness / Data Consistency
  - Compare Cost
  - **Owner:** TBD
- **Result Analysis**
  - Analyze monitoring results
  - Identify trade-offs between v1 and v2
  - Evaluate promotion / rollback decisions
  - **Owner:** TBD
- **Final System Verification**
  - Verify integrated system
  - Verify canary deployment
  - Verify monitoring / dashboard
  - Prepare final demonstration
  - **Owner:** TBD
- **Final Delivery**
  - Final Demo
  - Project Documentation
  - Final Report
  - Final Presentation
  - **Owner:** TBD

## 2. Project Timeline

### Phase 1 — Baseline & Cloud Setup

| Task | Status |
|---|---|
| Google Cloud Environment | Completed |
| Baseline Deployment | Completed |
| Baseline Verification | Completed |

### Phase 2 — Infrastructure & Project Setup

| Task | Owner | Start Time | Due Time | Status |
|---|---|---|---|---|
| CI/CD | pauriccarr | Sep 18 | TBD | In Progress |
| NoSQL Migration | pauriccarr | Sep 20 | TBD | In Progress |
| GitHub / GCP Integration | Shared | — | — | Completed |

### Phase 3 — Agentification

| Task | Owner | Start Time | Due Time | Status |
|---|---|---|---|---|
| Recommendation Service v1 Analysis | jingyawang | Sep 21 | Sep 21 | Completed |
| Agent v2 Design | jingyawang | Sep 21 | Sep 22 | Completed |
| Model / Runtime / Tool Selection | jingyawang | Sep 22 | Sep 22 | Completed |
| Agentification Technical Plan & D2 Preparation | jingyawang | Sep 22 | Sep 23 | In Progress |
| Agent v2 Implementation | jingyawang | Sep 24 | Oct 6 | Not Started |
| Agent v2 Testing | jingyawang | Sep 24 | Oct 6 | Not Started |

### Phase 4 — Canary Integration

| Task | Owner | Start Time | Due Time | Status |
|---|---|---|---|---|
| v1 / v2 Deployment | shared | Sep 24 | Oct 6 | Not Started |
| Canary Routing | shared | TBD | TBD | Not Started |
| Traffic Splitting | shared | TBD | TBD | Not Started |
| Promotion / Rollback | shared | TBD | TBD | Not Started |

### Phase 5 — Observability & Monitoring

| Task | Owner | Start Time | Due Time | Status |
|---|---|---|---|---|
| Instrumentation / Telemetry | pauriccarr | Sep 24 | Oct 6 | In Progress |
| Metrics Collection | pauriccarr | Sep 24 | Oct 6 | In Progress |
| Google Cloud Monitoring | pauriccarr | Sep 24 | Oct 6 | In Progress |
| Dashboard & Widgets | pauriccarr | Sep 24 | Oct 6 | In Progress |
| Cost Monitoring | pauriccarr | Sep 24 | Oct 6 | In Progress |

### Phase 6 — Experiment, Evaluation & Delivery

| Task | Owner | Start Time | Due Time | Status |
|---|---|---|---|---|
| Canary Experiment | TBD | TBD | TBD | TBD |
| v1 vs v2 Evaluation | TBD | TBD | TBD | TBD |
| Result Analysis | TBD | TBD | TBD | TBD |
| Final System Verification | TBD | TBD | TBD | TBD |
| Final Delivery | TBD | TBD | TBD | TBD |
