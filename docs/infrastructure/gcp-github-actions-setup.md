# GitHub Actions to GCP/GKE Configuration

## 1. Purpose

This document records the configuration used to connect the GitHub Actions workflows in this repository to Google Cloud Platform (GCP) and Google Kubernetes Engine (GKE).

The integration uses GitHub OpenID Connect (OIDC) and Google Cloud Workload Identity Federation (WIF), allowing GitHub Actions to authenticate to Google Cloud without storing a long-lived service account key in the repository.

---

## 2. Authentication Architecture

The authentication flow is:

GitHub Actions  
→ GitHub OIDC Token  
→ Google Cloud Workload Identity Federation Provider  
→ Service Account Impersonation  
→ Google Cloud Project  
→ GKE Cluster

GitHub Actions obtains an OIDC token from GitHub. Google Cloud validates the token through the configured Workload Identity Provider. An authorized workflow can then impersonate the designated Google Cloud service account and access the GCP resources permitted to that service account.

---

## 3. Google Cloud Configuration

### Project

- Project ID: `coen6313-online-boutique`

### GKE

- Cluster name: `online-boutique`
- Region: `us-central1`

The GKE cluster may be deleted when it is not being used to avoid unnecessary cloud resource usage. The Workload Identity Federation and service account configuration are project-level configurations and remain available after the cluster is deleted.

When the cluster is recreated using the same project, cluster name, and region, the existing GitHub Actions configuration can be reused.

---

## 4. Service Account

A dedicated Google Cloud service account is used by GitHub Actions.

- Display name: `GitHub Actions`
- Service account ID: `github-actions`
- Service account email: `github-actions@coen6313-online-boutique.iam.gserviceaccount.com`
- Assigned role: `Kubernetes Engine Developer`

The service account provides the Google Cloud permissions used by the GitHub Actions workflows.

No long-lived service account JSON key is stored in this repository.

---

## 5. Workload Identity Federation

### Workload Identity Pool

- Display name: `GitHub Actions Pool`
- Pool ID: `github-actions-pool`

### Workload Identity Provider

- Display name: `GitHub Actions Provider`
- Provider ID: `github-actions-provider`
- Provider type: `OpenID Connect (OIDC)`
- Issuer: `https://token.actions.githubusercontent.com/`

### Attribute Mapping

The provider uses the following mappings:

- `google.subject` → `assertion.sub`
- `attribute.repository` → `assertion.repository`

### Repository Restriction

Authentication is restricted to this GitHub repository:

`pauriccarr/COEN6313`

Provider condition:

`assertion.repository == 'pauriccarr/COEN6313'`

This prevents workflows from unrelated GitHub repositories from using this Workload Identity Provider.

---

## 6. Service Account Impersonation

The GitHub repository identity is authorized to impersonate the `GitHub Actions` service account through Workload Identity Federation.

The repository attribute used for this authorization is:

- Attribute: `repository`
- Value: `pauriccarr/COEN6313`

This allows authenticated GitHub Actions workflows from this repository to obtain short-lived Google Cloud credentials through the service account.

---

## 7. GitHub Repository Secrets

The current GitHub Actions workflows use the following repository secrets:

- `GCP_PROJECT_ID`
- `GCP_SERVICE_ACCOUNT`
- `GCP_WORKLOAD_IDENTITY_PROVIDER`
- `GKE_CLUSTER`
- `GKE_REGION`

These secrets are configured under:

`GitHub Repository → Settings → Secrets and variables → Actions`

---

## 8. Verification

The GitHub-to-GCP integration was verified by manually running both workflows against the `online-boutique` GKE cluster.

- `Deploy to GKE` — successful
- `Undeploy to GKE` — successful

This confirmed that GitHub Actions can authenticate to GCP and access the configured GKE cluster through the current WIF and service account setup.