#!/usr/bin/env bash
# Hybrid flow: swapnil-provider-mongodb + provider-mongodbatlas-upbound
# Phase 0 preflight  -> checks running providers, starts only missing ones (with consent)
# Phase 1 Create Org -> prompt org name, auto-detect current IP, apply
# Phase 2 IP edit    -> optional add / optional remove (yes/no each)
# Phase 3 PC+Project -> render pc.yaml with secret=<orgName>, pro.yaml with new orgID
# Phase 4 Delete     -> menu: org only | project only | all | skip

set -u
set -o pipefail

# ---------- config ----------
SWAPNIL_REPO="/Users/swapnil-valmik.chaudhari/Desktop/23-april-Dev-hybrid-provider/swapnil-provider-mongodb"
UPBOUND_REPO="/Users/swapnil-valmik.chaudhari/Desktop/23-april-Dev-hybrid-provider/provider-mongodbatlas-upboud"
SWAPNIL_BIN="/tmp/provider-mongodb"
UPBOUND_BIN="/tmp/provider-mongodbatlas-upbound"
SWAPNIL_LOG="/tmp/provider-mongodb.log"
UPBOUND_LOG="/tmp/provider-upbound.log"
ORG_YAML="${SWAPNIL_REPO}/examples/organization/ip-access-organization-with.yaml"
PC_YAML="${UPBOUND_REPO}/pc.yaml"
PRO_YAML="${UPBOUND_REPO}/pro.yaml"
AWS_PROFILE_NAME="198927051560_InfraAdmin"
AWS_REGION_NAME="eu-central-1"
KMS_KEY_ID="arn:aws:kms:eu-central-1:198927051560:key/50aed21c-4e38-4e62-9907-f35911304563"

BASE_IPS=("165.225.120.93" "167.103.3.4" "165.225.120.94")

# Runtime vars (populated as the script progresses)
SWAPNIL_PID=""
UPBOUND_PID=""
SWAPNIL_STARTED_BY_SCRIPT=false
UPBOUND_STARTED_BY_SCRIPT=false
ORG_NAME=""
ORG_ID=""
SECRET_NAME=""
PROJECT_NAME=""
CURRENT_IPS=()

# ---------- colours ----------
C_BLUE='\033[1;34m'; C_GREEN='\033[1;32m'; C_YELLOW='\033[1;33m'; C_RED='\033[1;31m'; C_CYAN='\033[1;36m'; C_DIM='\033[2m'; C_RESET='\033[0m'
banner()        { printf "\n${C_BLUE}──▶ [%s]${C_RESET} ${C_CYAN}%s${C_RESET}\n" "$1" "$2"; }
ok()            { printf "${C_GREEN}  ✓ %s${C_RESET}\n" "$1"; }
warn()          { printf "${C_YELLOW}  ! %s${C_RESET}\n" "$1"; }
err()           { printf "${C_RED}  ✗ %s${C_RESET}\n" "$1"; }
status_header() { printf "${C_DIM}  ── status: %s${C_RESET}\n" "$1"; }
status_kv()     { printf "${C_DIM}     %-18s : %s${C_RESET}\n" "$1" "$2"; }
ask()    { local prompt="$1" default="${2:-}" reply; if [[ -n "$default" ]]; then read -r -p "$prompt [$default]: " reply; echo "${reply:-$default}"; else read -r -p "$prompt: " reply; echo "$reply"; fi; }
ask_yn() { local prompt="$1" reply; read -r -p "$prompt [y/N]: " reply; [[ "$reply" =~ ^[Yy]$ ]]; }

# ---------- status helpers ----------
status_providers() {
  status_header "Providers"
  if [[ -n "$SWAPNIL_PID" ]] && ps -p "$SWAPNIL_PID" >/dev/null 2>&1; then status_kv "swapnil PID" "$SWAPNIL_PID (running)"; else status_kv "swapnil PID" "not running"; fi
  if [[ -n "$UPBOUND_PID" ]] && ps -p "$UPBOUND_PID" >/dev/null 2>&1; then status_kv "upbound PID" "$UPBOUND_PID (running)"; else status_kv "upbound PID" "not running"; fi
  status_kv "swapnil log" "$SWAPNIL_LOG"
  status_kv "upbound log" "$UPBOUND_LOG"
}

status_org() {
  status_header "Organization $ORG_NAME"
  local ready state orgid sname sarn storage ipcount keyid roles lastup
  ready=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
  state=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.state}' 2>/dev/null)
  orgid=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.orgID}' 2>/dev/null)
  sname=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.secretName}' 2>/dev/null)
  sarn=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.secretARN}' 2>/dev/null)
  storage=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.secretStorageType}' 2>/dev/null)
  ipcount=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.ipAccessEntryCount}' 2>/dev/null)
  keyid=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.createdWithAPIKeyID}' 2>/dev/null)
  roles=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.apiKeyRoles}' 2>/dev/null)
  lastup=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.lastIPAccessUpdate}' 2>/dev/null)
  status_kv "orgID" "${orgid:-<unset>}"
  status_kv "state" "${state:-<unset>}"
  status_kv "ready" "${ready:-<unset>}"
  status_kv "secret name" "${sname:-<unset>}"
  status_kv "secret ARN" "${sarn:-<unset>}"
  status_kv "secret storage" "${storage:-<unset>}"
  status_kv "IP entry count" "${ipcount:-0}"
  status_kv "provisioned IPs" "$(IFS=', '; echo "${CURRENT_IPS[*]}")"
  status_kv "created API key" "${keyid:-<unset>} ${roles}"
  status_kv "last IP update" "${lastup:-<unset>}"
}

status_pc() {
  status_header "ProviderConfig default-aws-secrets"
  local region secret kms
  region=$(kubectl get providerconfig.mongodbatlas.crossplane.io default-aws-secrets -o jsonpath='{.spec.credentials.aws.secretsManager.region}' 2>/dev/null)
  secret=$(kubectl get providerconfig.mongodbatlas.crossplane.io default-aws-secrets -o jsonpath='{.spec.credentials.aws.secretsManager.secretName}' 2>/dev/null)
  kms=$(kubectl get providerconfig.mongodbatlas.crossplane.io default-aws-secrets -o jsonpath='{.spec.credentials.aws.secretsManager.kmsKeyId}' 2>/dev/null)
  status_kv "source" "AWS Secrets Manager"
  status_kv "region" "${region:-<unset>}"
  status_kv "secret name" "${secret:-<unset>}"
  status_kv "kms key" "${kms:-<unset>}"
}

status_project() {
  status_header "Project $PROJECT_NAME"
  local ready synced orgid extname async lastop
  ready=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
  synced=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.status.conditions[?(@.type=="Synced")].status}' 2>/dev/null)
  orgid=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.spec.forProvider.orgId}' 2>/dev/null)
  extname=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.metadata.annotations.crossplane\.io/external-name}' 2>/dev/null)
  async=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.status.conditions[?(@.type=="AsyncOperation")].reason}' 2>/dev/null)
  lastop=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.status.conditions[?(@.type=="LastAsyncOperation")].reason}' 2>/dev/null)
  status_kv "ready" "${ready:-<unset>}"
  status_kv "synced" "${synced:-<unset>}"
  status_kv "orgId (spec)" "${orgid:-<unset>}"
  status_kv "external-name" "${extname:-<unset>}"
  status_kv "async op" "${async:-<unset>}"
  status_kv "last async" "${lastop:-<unset>}"
}

# ---------- phase 0: preflight ----------
detect_listener_pid() {
  local port="$1"
  lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | head -1
}

build_binary() {
  local repo="$1" out="$2" label="$3"
  [[ -d "$repo" ]] || { err "Repo not found: $repo"; return 1; }
  command -v go >/dev/null 2>&1 || { err "go not installed in PATH"; return 1; }
  printf "  building %s from %s …\n" "$label" "$repo"
  ( cd "$repo" && go build -o "$out" ./cmd/provider ) || { err "go build failed for $label"; return 1; }
  [[ -x "$out" ]] || { err "Binary not produced: $out"; return 1; }
  ok "built $label -> $out"
}

preflight() {
  banner "PHASE 0" "Preflight — checking tooling + running providers"
  export AWS_PROFILE="$AWS_PROFILE_NAME"
  export AWS_REGION="$AWS_REGION_NAME"
  aws sts get-caller-identity >/dev/null 2>&1 || { err "AWS creds not valid — refresh and retry"; exit 1; }
  ok "AWS creds OK (profile=$AWS_PROFILE_NAME)"
  kubectl version --request-timeout=5s >/dev/null 2>&1 || { err "kubectl not reachable"; exit 1; }
  ok "kubectl cluster reachable"
  if [[ ! -x "$SWAPNIL_BIN" ]]; then
    warn "Missing $SWAPNIL_BIN"
    if ask_yn "Build swapnil-provider-mongodb now?"; then
      build_binary "$SWAPNIL_REPO" "$SWAPNIL_BIN" "swapnil-provider-mongodb" || exit 1
    else
      err "Cannot continue without $SWAPNIL_BIN"; exit 1
    fi
  fi
  if [[ ! -x "$UPBOUND_BIN" ]]; then
    warn "Missing $UPBOUND_BIN"
    if ask_yn "Build provider-mongodbatlas-upbound now?"; then
      build_binary "$UPBOUND_REPO" "$UPBOUND_BIN" "provider-mongodbatlas-upbound" || exit 1
    else
      err "Cannot continue without $UPBOUND_BIN"; exit 1
    fi
  fi
  ok "Both binaries present"

  SWAPNIL_PID=$(detect_listener_pid 8081 || true)
  UPBOUND_PID=$(detect_listener_pid 8082 || true)
  if [[ -n "$SWAPNIL_PID" ]]; then ok "swapnil-provider-mongodb already running (PID $SWAPNIL_PID) — will not start it"; else warn "swapnil-provider-mongodb NOT running"; fi
  if [[ -n "$UPBOUND_PID" ]]; then ok "provider-mongodbatlas-upbound already running (PID $UPBOUND_PID) — will not start it"; else warn "provider-mongodbatlas-upbound NOT running"; fi

  if [[ -z "$SWAPNIL_PID" || -z "$UPBOUND_PID" ]]; then
    if ask_yn "Start the missing provider(s) now?"; then
      start_missing_providers
    else
      err "Cannot continue without both providers running"; exit 1
    fi
  fi
  status_providers
}

start_missing_providers() {
  banner "PHASE 0b" "Starting missing providers"
  if [[ -z "$SWAPNIL_PID" ]]; then
    : > "$SWAPNIL_LOG"
    (AWS_PROFILE="$AWS_PROFILE_NAME" AWS_REGION="$AWS_REGION_NAME" "$SWAPNIL_BIN" --debug >>"$SWAPNIL_LOG" 2>&1) &
    SWAPNIL_PID=$!
    SWAPNIL_STARTED_BY_SCRIPT=true
    printf "  swapnil starting (PID=%s)…" "$SWAPNIL_PID"
    wait_for_log "$SWAPNIL_LOG" "swapnil-provider-mongodb" || exit 1
  fi
  if [[ -z "$UPBOUND_PID" ]]; then
    : > "$UPBOUND_LOG"
    (AWS_PROFILE="$AWS_PROFILE_NAME" AWS_REGION="$AWS_REGION_NAME" "$UPBOUND_BIN" --debug \
       --terraform-provider-source=terraform-providers/mongodbatlas \
       --terraform-version=1.5.7 \
       --terraform-provider-version=1.9.0 \
       --metrics-bind-address=":8082" --webhook-port=9444 >>"$UPBOUND_LOG" 2>&1) &
    UPBOUND_PID=$!
    UPBOUND_STARTED_BY_SCRIPT=true
    printf "  upbound starting (PID=%s)…" "$UPBOUND_PID"
    wait_for_log "$UPBOUND_LOG" "provider-mongodbatlas-upbound" || exit 1
  fi
}

wait_for_log() {
  local log="$1" label="$2" deadline=$(( $(date +%s) + 60 ))
  while (( $(date +%s) < deadline )); do
    if grep -q "Starting workers" "$log" 2>/dev/null; then printf "\n"; ok "$label ready"; return 0; fi
    printf "."; sleep 1
  done
  printf "\n"; err "$label did not start within 60s (see $log)"; return 1
}

# ---------- phase 1: create org ----------
create_org() {
  banner "PHASE 1" "Create Organization (swapnil-provider)"
  ORG_NAME=$(ask "Org name (secret will reuse this name)" "swap-v15")

  # auto-detect current IP and add to access list
  local detected=""
  detected=$(curl -s --max-time 5 ifconfig.me 2>/dev/null || true)
  if [[ -z "$detected" ]]; then
    warn "Could not auto-detect current IP"
    detected=$(ask "Enter current IP manually")
    [[ -z "$detected" ]] && { err "IP required"; exit 1; }
  else
    ok "Detected current IP: $detected (will be added to access list)"
  fi

  CURRENT_IPS=("${BASE_IPS[@]}" "$detected")
  render_org_yaml

  printf "  applying Organization %s\n" "$ORG_NAME"
  printf "    metadata.name = %s\n" "$ORG_NAME"
  printf "    secretName    = %s (AWS)\n" "$ORG_NAME"
  printf "    IPs           = %s\n" "${CURRENT_IPS[*]}"
  kubectl apply -f "$ORG_YAML" >/dev/null
  wait_org_ready "$ORG_NAME"
  ORG_ID=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.orgID}')
  SECRET_NAME=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.secretName}')
  ok "Org ACTIVE — orgID=$ORG_ID  secret=$SECRET_NAME"
  status_org
}

render_org_yaml() {
  {
    echo "apiVersion: organization.mongodb.swapnil.io/v1alpha1"
    echo "kind: Organization"
    echo "metadata:"
    echo "  name: $ORG_NAME"
    echo "spec:"
    echo "  forProvider:"
    echo "    ownerID: \"68933765952cea244d470efb\""
    echo "    apiKey:"
    echo "      description: \"Pure AWS organization API key - no Kubernetes secrets\""
    echo "      roles:"
    echo "        - \"ORG_OWNER\""
    echo "    awsSecretsConfig:"
    echo "      region: \"$AWS_REGION_NAME\""
    echo "      secretName: \"$ORG_NAME\""
    echo "      kmsKeyId: \"$KMS_KEY_ID\""
    echo "    networkAccessConfig:"
    echo "      enabled: true"
    echo "      autoCleanup: true"
    echo "      apiAccessListRequired: true"
    echo "      ips:"
    for ip in "${CURRENT_IPS[@]}"; do echo "        - ip: \"$ip\""; done
    echo "  providerConfigRef:"
    echo "    name: atlas-provider-aws-only"
    echo "  deletionPolicy: Delete"
    echo "  managementPolicies: [\"*\"]"
  } > "$ORG_YAML"
}

wait_org_ready() {
  local name="$1" deadline=$(( $(date +%s) + 180 ))
  printf "  waiting for Ready=True State=ACTIVE…"
  while (( $(date +%s) < deadline )); do
    local ready state
    ready=$(kubectl get organization "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
    state=$(kubectl get organization "$name" -o jsonpath='{.status.atProvider.state}' 2>/dev/null || true)
    if [[ "$ready" == "True" && "$state" == "ACTIVE" ]]; then printf "\n"; return 0; fi
    printf "."; sleep 3
  done
  printf "\n"; err "Org did not reach ACTIVE within 180s"
  kubectl get organization "$name" -o jsonpath='{.status.conditions}' 2>&1 || true
  exit 1
}

show_ips_numbered() {
  printf "  current IPs:\n"
  local i=1
  for ip in "${CURRENT_IPS[@]}"; do printf "    %d) %s\n" "$i" "$ip"; i=$((i+1)); done
}

wait_ip_count() {
  local target="$1" deadline=$(( $(date +%s) + 60 ))
  printf "  waiting for provisionedIPs count=%d…" "$target"
  while (( $(date +%s) < deadline )); do
    local count
    count=$(kubectl get organization "$ORG_NAME" -o jsonpath='{.status.atProvider.provisionedIPs}' 2>/dev/null | python3 -c "import sys,json;d=sys.stdin.read();print(len(json.loads(d)) if d else 0)" 2>/dev/null || echo 0)
    if [[ "$count" == "$target" ]]; then printf "\n"; ok "IPs synced (count=$count)"; return 0; fi
    printf "."; sleep 3
  done
  printf "\n"; warn "IP count did not converge within 60s (may still be reconciling)"
}

# ---------- phase 2: IP add / remove ----------
ip_edit_phase() {
  banner "PHASE 2" "IP access list — add/remove (skip if not needed)"
  show_ips_numbered

  if ask_yn "Do you want to ADD more IP(s)?"; then
    while true; do
      local newip
      newip=$(ask "IP to add (blank to stop adding)")
      [[ -z "$newip" ]] && break
      CURRENT_IPS+=("$newip")
      printf "  ──▶ Adding IP %s (new total: %d)\n" "$newip" "${#CURRENT_IPS[@]}"
      render_org_yaml
      kubectl apply -f "$ORG_YAML" >/dev/null
      wait_ip_count "${#CURRENT_IPS[@]}"
    done
  else
    warn "skipping add"
  fi

  if ask_yn "Do you want to REMOVE any IP(s)?"; then
    while true; do
      show_ips_numbered
      local idx
      idx=$(ask "Number to remove (blank to stop removing)")
      [[ -z "$idx" ]] && break
      [[ ! "$idx" =~ ^[0-9]+$ ]] && { warn "Not a number"; continue; }
      (( idx < 1 || idx > ${#CURRENT_IPS[@]} )) && { warn "Out of range"; continue; }
      local target="${CURRENT_IPS[$((idx-1))]}"
      local new=()
      for i in "${!CURRENT_IPS[@]}"; do (( i != idx-1 )) && new+=("${CURRENT_IPS[$i]}"); done
      CURRENT_IPS=("${new[@]}")
      printf "  ──▶ Removing IP %s (new total: %d)\n" "$target" "${#CURRENT_IPS[@]}"
      render_org_yaml
      kubectl apply -f "$ORG_YAML" >/dev/null
      wait_ip_count "${#CURRENT_IPS[@]}"
    done
  else
    warn "skipping remove"
  fi
  status_org
}

# ---------- phase 3: PC + Project ----------
create_pc_and_project() {
  banner "PHASE 3.1" "Render pc.yaml with secretName=$SECRET_NAME"
  cat > "$PC_YAML" <<EOF
apiVersion: mongodbatlas.crossplane.io/v1beta1
kind: ProviderConfig
metadata:
  name: default-aws-secrets
spec:
  credentials:
    source: AWS
    aws:
      secretsManager:
        region: $AWS_REGION_NAME
        secretName: $SECRET_NAME
        kmsKeyId: $KMS_KEY_ID
EOF
  ok "rendered $PC_YAML"
  if ! ask_yn "Apply ProviderConfig default-aws-secrets now?"; then
    warn "skipping ProviderConfig apply — cannot continue without it"
    return 1
  fi
  kubectl apply -f "$PC_YAML" >/dev/null
  ok "ProviderConfig default-aws-secrets applied"
  status_pc

  banner "PHASE 3.2" "Render pro.yaml with orgId=$ORG_ID"
  PROJECT_NAME=$(ask "Project name" "example-project")
  cat > "$PRO_YAML" <<EOF
apiVersion: mongodbatlas.crossplane.io/v1alpha1
kind: Project
metadata:
  name: $PROJECT_NAME
spec:
  forProvider:
    orgId: "$ORG_ID"
  providerConfigRef:
    name: default-aws-secrets
EOF
  ok "rendered $PRO_YAML"
  if ! ask_yn "Apply Project $PROJECT_NAME now?"; then
    warn "skipping Project apply"
    return 0
  fi
  kubectl apply -f "$PRO_YAML" >/dev/null
  printf "  waiting for Project Ready=True Synced=True…"
  local deadline=$(( $(date +%s) + 180 ))
  while (( $(date +%s) < deadline )); do
    local ready synced
    ready=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
    synced=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.status.conditions[?(@.type=="Synced")].status}' 2>/dev/null || true)
    if [[ "$ready" == "True" && "$synced" == "True" ]]; then printf "\n"; break; fi
    printf "."; sleep 3
  done
  local extname
  extname=$(kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" -o jsonpath='{.metadata.annotations.crossplane\.io/external-name}' 2>/dev/null || true)
  ok "Project Ready — external-name=$extname"
  status_project
}

# ---------- phase 4: delete menu ----------
delete_menu() {
  banner "PHASE 4" "Delete resources — choose what to remove"
  echo "  Current stack:"
  echo "    Org           : $ORG_NAME ($ORG_ID)"
  echo "    AWS secret    : $SECRET_NAME"
  echo "    ProviderConfig: default-aws-secrets"
  echo "    Project       : $PROJECT_NAME"
  echo
  echo "  [1] Delete Organization only (will also delete AWS secret)"
  echo "  [2] Delete Project only"
  echo "  [3] Delete ALL in dependency order (Project → PC → Org)"
  echo "  [s] Skip — keep everything"
  local choice
  choice=$(ask "Choice")
  case "$choice" in
    1) delete_org ;;
    2) delete_project ;;
    3) delete_project; delete_pc; delete_org ;;
    s|S|"") warn "skipping delete — resources left intact" ;;
    *)    warn "unknown choice — skipping" ;;
  esac
}

delete_project() {
  banner "PHASE 4.p" "Delete Project $PROJECT_NAME"
  kubectl delete project.mongodbatlas.crossplane.io "$PROJECT_NAME" --wait=true --timeout=180s 2>&1 | sed 's/^/    /' || true
  ok "Project delete requested"
  status_header "after Project delete"
  if kubectl get project.mongodbatlas.crossplane.io "$PROJECT_NAME" >/dev/null 2>&1; then status_kv "project" "still present"; else status_kv "project" "NotFound"; fi
}

delete_pc() {
  banner "PHASE 4.pc" "Delete ProviderConfig default-aws-secrets"
  kubectl delete providerconfig.mongodbatlas.crossplane.io default-aws-secrets --wait=true --timeout=60s 2>&1 | sed 's/^/    /' || true
  ok "ProviderConfig delete requested"
  status_header "after PC delete"
  if kubectl get providerconfig.mongodbatlas.crossplane.io default-aws-secrets >/dev/null 2>&1; then status_kv "providerconfig" "still present"; else status_kv "providerconfig" "NotFound"; fi
}

delete_org() {
  banner "PHASE 4.o" "Delete Organization $ORG_NAME (provider removes IPs, AWS secret, force-finalizer)"
  kubectl delete organization "$ORG_NAME" --wait=false 2>&1 | sed 's/^/    /' || true
  local deadline=$(( $(date +%s) + 180 ))
  printf "  waiting for CR NotFound…"
  while (( $(date +%s) < deadline )); do
    if ! kubectl get organization "$ORG_NAME" >/dev/null 2>&1; then printf "\n"; ok "Organization CR deleted"; break; fi
    printf "."; sleep 3
  done
  status_header "after Org delete"
  if kubectl get organization "$ORG_NAME" >/dev/null 2>&1; then status_kv "CR" "still present"; else status_kv "CR" "NotFound"; fi
  if aws secretsmanager describe-secret --secret-id "$SECRET_NAME" --region "$AWS_REGION_NAME" >/dev/null 2>&1; then
    status_kv "AWS secret" "still present"
  else
    status_kv "AWS secret" "ResourceNotFoundException (deleted)"
  fi
  status_kv "atlas org" "not verified server-side (check UI if needed)"
}

# ---------- cleanup: stop providers only if this script started them ----------
cleanup() {
  banner "CLEANUP" "Stop providers (only those started by this script)"
  if $SWAPNIL_STARTED_BY_SCRIPT || $UPBOUND_STARTED_BY_SCRIPT; then
    if ask_yn "Stop the provider(s) started by this script?"; then
      $SWAPNIL_STARTED_BY_SCRIPT && { kill "$SWAPNIL_PID" 2>/dev/null || true; sleep 1; kill -9 "$SWAPNIL_PID" 2>/dev/null || true; ok "swapnil stopped"; }
      $UPBOUND_STARTED_BY_SCRIPT && { kill "$UPBOUND_PID" 2>/dev/null || true; sleep 1; kill -9 "$UPBOUND_PID" 2>/dev/null || true; ok "upbound stopped"; }
    else
      warn "Providers left running — PIDs: swapnil=$SWAPNIL_PID upbound=$UPBOUND_PID"
    fi
  else
    ok "No providers were started by this script — leaving pre-existing processes untouched"
  fi
  echo "  logs: $SWAPNIL_LOG , $UPBOUND_LOG"
}

# ---------- main ----------
trap 'err "Aborted — check provider state (swapnil=${SWAPNIL_PID:-?}, upbound=${UPBOUND_PID:-?})"' ERR

preflight
create_org
ip_edit_phase
create_pc_and_project
delete_menu
cleanup

banner "DONE" "Hybrid flow complete"
