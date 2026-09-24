# Canary Traffic Splitting — Design Draft

## 1. Objective

The goal is to gradually route production-like traffic from v1 to v2 instead of replacing v1 with v2 immediately.

The planned canary promotion stages are:

0% → 10% → 25% → 50% → 100%

At each stage, v2 will be evaluated using the predefined canary metrics before promotion to the next stage.

If any acceptance criterion is violated, traffic will be rolled back to v1.

---

## 2. Current System Status

The current project already provides:

### Deployment Platform
- Google Kubernetes Engine (GKE)
- Kubernetes manifests

### CI/CD
- GitHub Actions

The deployment workflow already exposes an:

`agent_traffic_percent`

parameter with selectable values:

- 0%
- 10%
- 25%
- 50%
- 75%
- 100%

However, this parameter still needs to be connected to the actual Kubernetes traffic-splitting mechanism.

### Monitoring
- Prometheus
- Grafana

These components will be used to collect and visualize the canary evaluation metrics.

---

## 3. Proposed Canary Architecture

The proposed architecture uses two Kubernetes Deployments behind a common service interface.

                    Incoming Requests
                           |
                           v
                 Kubernetes Service
                           |
                 Traffic Distribution
                    /             \
                   /               \
                  v                 v
        Checkout Service v1    Checkout Agent v2
          Deployment             Deployment
             |                       |
          v1 Pods                  v2 Pods

The two versions remain deployed simultaneously during the canary process.

---

## 4. Traffic Splitting Strategy

The initial implementation will use Kubernetes replica ratios to approximate the desired traffic distribution.

Example:

### 10% Canary

v1 replicas : v2 replicas

9 : 1

Approximate traffic distribution:

- v1 → 90%
- v2 → 10%

### 25% Canary

v1 replicas : v2 replicas

3 : 1

Approximate traffic distribution:

- v1 → 75%
- v2 → 25%

### 50% Canary

v1 replicas : v2 replicas

1 : 1

Approximate traffic distribution:

- v1 → 50%
- v2 → 50%

### 100% Promotion

- v1 → 0%
- v2 → 100%

---

## 5. Important Technical Limitation

Kubernetes replica ratios provide approximate traffic distribution rather than strict request-level weighted routing.

For example:

9 v1 pods + 1 v2 pod

does not guarantee that exactly 10% of requests will reach v2.

For the current prototype, replica-based traffic splitting is used as the initial lightweight approach.

If precise weighted routing becomes necessary, the architecture can later be extended using tools such as:

- Istio weighted routing
- Argo Rollouts
- Flagger

---

## 6. GitHub Actions Integration

The existing GitHub Actions input:

`agent_traffic_percent`

will control the selected canary stage.

Planned workflow:

GitHub Actions
      |
      | agent_traffic_percent
      v
Determine desired v1/v2 configuration
      |
      v
Update Kubernetes deployments
      |
      v
Deploy to GKE
      |
      v
Run workload
      |
      v
Collect metrics
      |
      v
Evaluate canary criteria
     / \
 PASS   FAIL
  |       |
  v       v
Promote  Rollback to v1

---

## 7. Canary Promotion

Example promotion sequence:

### Stage 1

10% → v2  
90% → v1

Run the workload for a fixed duration and collect metrics.

If all criteria pass:

10% → 25%

### Stage 2

25% → v2  
75% → v1

Evaluate the same metrics using the same workload.

If all criteria pass:

25% → 50%

### Stage 3

50% → v2  
50% → v1

If all criteria pass:

50% → 100%

At 100%, v2 becomes the fully promoted version.

---

## 8. Rollback

If any canary criterion fails at any stage:

Canary Stage
     |
     v
Criterion Violation
     |
     v
Stop Promotion
     |
     v
Route Traffic Back to v1

Target rollback state:

- v1 → 100%
- v2 → 0%

For the final demonstration, a failure can be intentionally triggered to demonstrate rollback behavior.

---

## 9. Selected Traffic-Splitting Approach

**Kubernetes replica-based traffic splitting**

Two Kubernetes Deployments will run behind one Kubernetes Service:

- **v1:** Deterministic Checkout Service
- **v2:** Agentified Checkout Service

The ratio of v1 and v2 replicas will be adjusted according to the selected canary stage.


