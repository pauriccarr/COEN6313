# COEN6313 PROJECT — Google Online Boutique

## 1. PROJECT PHASES & TASK OWNERSHIP

## PHASE 1 — BASELINE & CLOUD SETUP

### Google Cloud Environment

Set up the Google Cloud environment required to deploy and manage the Online Boutique application, including the GCP project, GKE environment, and local access to cloud resources.

- **Subtasks:**
  - Create / configure GCP project
  - Configure GKE environment
  - Configure local access to GCP / GKE

### Baseline Deployment

Deploy the original Google Online Boutique to GKE and verify that the Kubernetes workloads and application are running correctly before any project-specific modifications are introduced.

- **Subtasks:**
  - Deploy the original Online Boutique
  - Verify Kubernetes workloads
  - Verify the original application is working

### Baseline Verification

Confirm that the original microservices and application behavior are working as expected, providing a stable baseline for later development and comparison.

- **Subtasks:**
  - Verify original microservices are running
  - Confirm the baseline system before modification

## PHASE 2 — INFRASTRUCTURE & PROJECT SETUP

### CI/CD

Set up GitHub Actions workflows to automate deployment and undeployment of the application and verify that the deployment process can be executed reliably through the repository.

- **Subtasks:**
  - Configure GitHub Actions workflows
  - Deploy workflow
  - Undeploy workflow
  - Verify deployment automation

### NoSQL Migration

Review the existing Cart Service storage implementation, migrate the selected storage backend from Redis to MongoDB, integrate it with the Cart Service, and verify that cart data operations continue to work correctly.

- **Subtasks:**
  - Review existing Cart Service storage
  - Migrate Redis → MongoDB
  - Integrate MongoDB with Cart Service
  - Verify cart data operations

### GitHub / GCP Integration

Configure secure authentication and access between GitHub Actions and Google Cloud/GKE so that automated workflows can interact with the target Kubernetes cluster.

- **Subtasks:**
  - Configure GitHub → GCP authentication
  - Configure GKE access from GitHub Actions
  - Verify GitHub Actions can access the GKE cluster

## PHASE 3 — AGENTIFICATION

### Recommendation Service v1 Analysis

Analyze the existing Recommendation Service, including its API, recommendation logic, dependencies, and limitations, to identify the main decision point and opportunities for agentification.

- **Subtasks:**
  - Understand v1 input / output
  - Understand existing recommendation logic
  - Identify limitations and opportunities for agentification

### Agent v2 Design

Design the AI Agent version of the Recommendation Service, including its capabilities, workflow, model-driven decision process, and interaction with external tools or backend services, while preserving the required service interface.

- **Subtasks:**
  - Define Agent v2 capabilities
  - Design Agent v2 workflow
  - Define model-driven decision logic
  - Preserve the required service API/interface

### Model / Runtime / Tool Selection

Evaluate and select an appropriate model, model access/runtime approach, and required Agent tools or backend services. Candidate solutions can be tested based on factors such as structured output, tool-calling capability, latency, cost, and integration complexity.

- **Subtasks:**
  - Select LLM / model
  - Select model runtime / API
  - Define required tools / backend calls

### Agent v2 Implementation

Implement the designed Agent workflow, integrate model access and required tools/backend services, and connect Agent v2 with the existing Online Boutique architecture.

- **Subtasks:**
  - Implement agent logic
  - Integrate model access
  - Integrate required tools / backend services
  - Integrate Agent v2 with Online Boutique

### Agent v2 Testing

Test the basic behavior of Agent v2, verify input/output and API compatibility, validate model and tool interactions, and confirm that the Agent is ready for deployment.

- **Subtasks:**
  - Verify input / output behavior
  - Verify compatibility with the existing API
  - Test basic agent behavior
  - Prepare Agent v2 for deployment

## PHASE 4 — CANARY INTEGRATION

### v1 / v2 Deployment

Deploy Recommendation Service v1 and Recommendation Agent v2 simultaneously and verify that both versions can operate correctly in the same environment.

- **Subtasks:**
  - Deploy Recommendation Service v1
  - Deploy Recommendation Agent v2
  - Verify v1 and v2 can run simultaneously

### Canary Routing

Configure a routing mechanism that can direct incoming recommendation requests to either v1 or v2 and verify that both versions can receive traffic.

- **Subtasks:**
  - Configure traffic routing between v1 and v2
  - Verify requests can reach both versions

### Traffic Splitting

Define and execute controlled canary traffic stages to gradually shift traffic from v1 to v2 while maintaining controlled testing conditions.

- **Subtasks:**
  - Define canary traffic stages
  - Run controlled traffic splits
  - Support gradual migration toward v2

### Promotion / Rollback

Define the conditions and mechanisms for increasing v2 traffic when it performs acceptably and returning traffic to v1 when the required conditions are not met.

- **Subtasks:**
  - Define promotion conditions
  - Define rollback conditions
  - Verify promotion mechanism
  - Verify rollback mechanism

## PHASE 5 — OBSERVABILITY & MONITORING

### Instrumentation / Telemetry

Identify the telemetry required to observe system behavior and instrument the relevant services and Agent components using OpenTelemetry or other required mechanisms.

- **Subtasks:**
  - Identify required telemetry
  - Instrument relevant services / agent
  - Configure traces / metrics as needed
  - OpenTelemetry integration

### Metrics Collection

Collect application, service, and v1/v2 metrics using Prometheus and verify that the required metrics are available for downstream monitoring and analysis.

- **Subtasks:**
  - Collect application / service metrics
  - Collect v1 / v2 metrics
  - Configure Prometheus
  - Verify metrics are available for monitoring

### Google Cloud Monitoring

Integrate the required metrics with Google Cloud Monitoring and verify that monitoring data can be accessed and used for system observation and evaluation.

- **Subtasks:**
  - Configure required monitoring resources
  - Connect relevant metrics
  - Verify monitoring data availability

### Dashboard & Widgets

Build monitoring dashboards and widgets to visualize key metrics such as availability/error rate, p95 latency, v1/v2 traffic, and cost, providing visibility into system and canary behavior.

- **Subtasks:**
  - Create monitoring dashboard
  - Availability / Error Rate widget
  - p95 Latency widget
  - Request / Traffic widget
  - Cost widget
  - Additional widgets if required

### Cost Monitoring

Track infrastructure and Agent/model usage costs, compare the cost of v1 and v2, and calculate cost-per-request or other cost metrics where applicable.

- **Subtasks:**
  - Configure cloud budget / cost monitoring
  - Monitor infrastructure cost
  - Monitor Agent / model usage cost
  - Compare v1 and v2 cost
  - Calculate / analyze cost per request if applicable

## PHASE 6 — EXPERIMENT, EVALUATION & DELIVERY

### Canary Experiment

Define a controlled experimental workload, execute the planned canary traffic stages under consistent conditions, and collect the data required for v1/v2 evaluation.

- **Subtasks:**
  - Define experiment workload
  - Run traffic stages
  - Keep workload conditions consistent
  - Collect experimental data

### v1 vs v2 Evaluation

Compare v1 and v2 using the selected evaluation criteria, including availability/error rate, p95 latency, correctness/data consistency, and cost.

- **Subtasks:**
  - Compare Availability / Error Rate
  - Compare p95 Latency
  - Compare Correctness / Data Consistency
  - Compare Cost


### Result Analysis

Analyze the experimental and monitoring results, identify trade-offs between the original microservice and Agent version, and evaluate whether the observed results support promotion or rollback decisions.

- **Subtasks:**
  - Analyze monitoring results
  - Identify trade-offs between v1 and v2
  - Evaluate promotion / rollback decisions


### Final System Verification

Verify that the complete integrated system—including v1/v2 deployment, canary behavior, monitoring, and dashboards—works correctly and is ready for the final demonstration.

- **Subtasks:**
  - Verify integrated system
  - Verify canary deployment
  - Verify monitoring / dashboard
  - Prepare final demonstration


### Final Delivery

Prepare and deliver the final project outputs, including the system demonstration, project documentation, final report, and final presentation.

- **Subtasks:**
  - Final Demo
  - Project Documentation
  - Final Report
  - Final Presentation

