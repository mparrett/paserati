#!/usr/bin/env bash
# Launch and tear down the dedicated box a perf session runs on.
#
# Usage:
#   scripts/perf-session-box.sh up   [options]     launch, wait, print the ssh line
#   scripts/perf-session-box.sh down [options]     terminate, then VERIFY by re-query
#   scripts/perf-session-box.sh status [options]   what is running, in every region
#
#   -r, --region R      default us-west-2
#   -t, --type T        instance type (default c7a.2xlarge; fixed-performance only)
#   -g, --go V          Go toolchain to install (default 1.26.0; match the corpus)
#   -w, --watchdog M    dead-man switch, minutes (default 300)
#   -n, --name N        tag/keypair/SG basename (default paserati-perf)
#   -s, --state DIR     where ids and the key live (default ./.perf-box)
#
# WHY THIS EXISTS
#
# The runbook documented teardown and not launch, so the launch recipe lived
# only inside old session transcripts. Reconstructing it on 2026-08-03 meant
# grepping a JSONL conversation log for `run-instances`. That is a real single
# point of failure for a procedure that spends money and, done wrong, leaves an
# instance running.
#
# Three things here are not incidental:
#
#   * NEVER burstable (t2/t3/t3a/t4g). CPU credit throttling changes the
#     machine's speed mid-session, which is variance indistinguishable from
#     signal.
#   * The dead-man switch is `shutdown -h +N` in user-data PLUS
#     --instance-initiated-shutdown-behavior terminate, so the halt is a
#     teardown rather than a stopped instance quietly accruing EBS charges.
#     reg-lisp had three consecutive runs fail and the trap is why they cost
#     nothing. A forgotten box is a live failure mode in this account.
#   * `down` verifies by RE-QUERY, not by trusting the terminate call, and it
#     checks orphaned volumes and unassociated elastic IPs too — an instance
#     going away does not mean its storage did.
set -uo pipefail

REGION=us-west-2
ITYPE=c7a.2xlarge
GOVER=1.26.0
WATCHDOG=300
NAME=paserati-perf
STATE=./.perf-box

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*" >&2; }

MODE="${1:-}"; shift 2>/dev/null || true
# --help as the first word is a request for help, not an unknown mode.
case "$MODE" in
  -h|--help|help|'')
    awk 'NR>1 && /^#/ {sub(/^# ?/,""); print; next} NR>1 {exit}' "$0"; exit 0;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    -r|--region)   REGION="${2:?}"; shift 2;;
    -t|--type)     ITYPE="${2:?}"; shift 2;;
    -g|--go)       GOVER="${2:?}"; shift 2;;
    -w|--watchdog) WATCHDOG="${2:?}"; shift 2;;
    -n|--name)     NAME="${2:?}"; shift 2;;
    -s|--state)    STATE="${2:?}"; shift 2;;
    -h|--help)     awk 'NR>1 && /^#/ {sub(/^# ?/,""); print; next} NR>1 {exit}' "$0"; exit 0;;
    *)             die "unknown argument: $1";;
  esac
done
command -v aws >/dev/null || die "aws not on PATH"

case "$ITYPE" in
  t2.*|t3.*|t3a.*|t4g.*)
    die "$ITYPE is burstable; CPU credit throttling makes it useless for timing";;
esac

case "$MODE" in
up)
  mkdir -p "$STATE"
  [ -f "$STATE/iid" ] && die "$STATE/iid exists — tear the old box down first"

  # AL2023 x86_64. Pinned rather than looked up: an AMI that changes under you
  # changes the kernel a session is measured on.
  AMI=ami-0b76d82b547c3c077

  MYIP="$(curl -s -4 https://checkip.amazonaws.com | tr -d '\n')" || die "cannot determine my IP"
  VPC="$(aws ec2 describe-vpcs --region "$REGION" --filters Name=isDefault,Values=true \
        --query 'Vpcs[0].VpcId' --output text)"
  note "region $REGION  type $ITYPE  vpc $VPC  ssh from ${MYIP}/32"

  # The local key file and the AWS-side key pair are two halves of one thing, and
  # AWS returns the private half exactly once — at creation. So the failure to
  # design against is holding one half without the other.
  #
  # This was `create-key-pair ... > "$STATE/key.pem" || note "reusing"`. The
  # redirect truncates the local key BEFORE the command's exit status is known, so
  # when the AWS-side pair already existed the create failed and the only copy of
  # the private key was destroyed in the same breath. The launch then succeeded in
  # every visible way — instance running, user-data complete — and refused every
  # ssh, which is a slow and expensive thing to diagnose. Write to a temp file and
  # move it into place only after the key material is in hand and looks real.
  key_ok() { [ -s "$1" ] && grep -q 'BEGIN.*PRIVATE KEY' "$1"; }

  if aws ec2 describe-key-pairs --region "$REGION" --key-names "$NAME" >/dev/null 2>&1; then
    if key_ok "$STATE/key.pem"; then
      note "keypair $NAME exists — reusing $STATE/key.pem"
    else
      # We hold the AWS half and not the usable local half, and AWS cannot reissue
      # it. A new pair is the only way forward. Safe because boxes here are
      # ephemeral — but note it would orphan a box still running under the old
      # key, which is why `down` is the documented way to end a session.
      note "keypair $NAME exists but $STATE/key.pem is missing or unusable — recreating"
      aws ec2 delete-key-pair --region "$REGION" --key-name "$NAME" >/dev/null \
        || die "cannot delete stale keypair $NAME"
    fi
  fi

  if ! key_ok "$STATE/key.pem"; then
    tmpkey="$STATE/key.pem.new"
    aws ec2 create-key-pair --region "$REGION" --key-name "$NAME" \
      --query 'KeyMaterial' --output text > "$tmpkey" 2>/dev/null \
      || { rm -f "$tmpkey"; die "cannot create keypair $NAME"; }
    key_ok "$tmpkey" || { rm -f "$tmpkey"; die "create-key-pair returned no usable key material"; }
    chmod 600 "$tmpkey"
    mv "$tmpkey" "$STATE/key.pem"
    note "keypair $NAME created"
  fi

  SG="$(aws ec2 create-security-group --region "$REGION" --group-name "$NAME" \
        --description "paserati perf session" --vpc-id "$VPC" \
        --query 'GroupId' --output text 2>/dev/null)" \
    || SG="$(aws ec2 describe-security-groups --region "$REGION" \
             --filters Name=group-name,Values="$NAME" --query 'SecurityGroups[0].GroupId' --output text)"
  aws ec2 authorize-security-group-ingress --region "$REGION" --group-id "$SG" \
    --protocol tcp --port 22 --cidr "${MYIP}/32" >/dev/null 2>&1
  echo "$SG" > "$STATE/sgid"

  cat > "$STATE/userdata.sh" <<EOF
#!/bin/bash
# Dead-man switch. Paired with --instance-initiated-shutdown-behavior terminate
# so this halt is a teardown, not a stopped instance accruing EBS charges.
shutdown -h +${WATCHDOG} "paserati perf watchdog: ${WATCHDOG}m cap" &
dnf -y install git jq tar gzip tmux >/tmp/pkg.log 2>&1
curl -fsSL https://go.dev/dl/go${GOVER}.linux-amd64.tar.gz -o /tmp/go.tgz
rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
echo 'export PATH=\$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
touch /tmp/READY
EOF

  IID="$(aws ec2 run-instances --region "$REGION" \
    --image-id "$AMI" --instance-type "$ITYPE" --count 1 \
    --key-name "$NAME" --security-group-ids "$SG" \
    --instance-initiated-shutdown-behavior terminate \
    --block-device-mappings '[{"DeviceName":"/dev/xvda","Ebs":{"VolumeSize":40,"VolumeType":"gp3","DeleteOnTermination":true}}]' \
    --user-data "file://$STATE/userdata.sh" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$NAME},{Key=Project,Value=paserati},{Key=Ephemeral,Value=true}]" \
    --query 'Instances[0].InstanceId' --output text)" || die "run-instances failed"
  echo "$IID" > "$STATE/iid"
  note "instance $IID — watchdog armed (${WATCHDOG}m, terminate on halt)"

  aws ec2 wait instance-running --region "$REGION" --instance-ids "$IID" || die "never reached running"
  IP="$(aws ec2 describe-instances --region "$REGION" --instance-ids "$IID" \
        --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)"
  echo "$IP" > "$STATE/ip"

  note "waiting for user-data to finish (go, tmux, jq)"
  for _ in $(seq 1 60); do
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 \
        -i "$STATE/key.pem" "ec2-user@$IP" 'test -f /tmp/READY' 2>/dev/null && break
    sleep 10
  done

  echo
  echo "  ssh -i $STATE/key.pem ec2-user@$IP"
  echo
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i "$STATE/key.pem" \
      "ec2-user@$IP" 'source /etc/profile.d/go.sh
       echo "  go:   $(go version)"
       echo "  cpu:  $(grep -m1 "model name" /proc/cpuinfo | cut -d: -f2 | xargs)"
       echo "  nproc: $(nproc)"
       T=$(curl -s -X PUT -H "X-aws-ec2-metadata-token-ttl-seconds: 60" http://169.254.169.254/latest/api/token)
       echo "  type: $(curl -s -H "X-aws-ec2-metadata-token: $T" http://169.254.169.254/latest/meta-data/instance-type)"' 2>/dev/null
  echo
  note "confirm the instance type above from metadata, not from what you asked for"
  ;;

down)
  IID="$(cat "$STATE/iid" 2>/dev/null || true)"
  SG="$(cat "$STATE/sgid" 2>/dev/null || true)"
  # Does AWS still know this instance? A terminated one ages out of the API
  # entirely, and the state file outlives it — `up` then refuses with "iid exists
  # — tear the old box down first", so `down` is exactly what you reach for, and
  # it used to take TEN MINUTES to do nothing: terminate-instances fails with
  # InvalidInstanceID.NotFound, and `wait instance-terminated` then retries that
  # same NotFound 40 times at 15s before giving up. Piped through tail it prints
  # nothing for the whole ten minutes, so it reads as a hang rather than as a
  # bounded retry. Ask first; skip both calls when there is nothing to terminate.
  # Checked by OUTPUT, not by exit status: describe-instances on an id AWS has
  # forgotten returns SUCCESS with an empty result, so `if ! aws ...` never fires
  # and the ten minutes happen anyway. Verified against the real vanished id
  # before trusting it.
  if [ -n "$IID" ]; then
    alive="$(aws ec2 describe-instances --region "$REGION" --instance-ids "$IID" \
      --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)"
    if [ -z "${alive//[[:space:]]/}" ]; then
      note "instance $IID no longer exists in $REGION — clearing stale state"
      IID=""
    fi
  fi
  if [ -n "$IID" ]; then
    aws ec2 terminate-instances --region "$REGION" --instance-ids "$IID" \
      --query 'TerminatingInstances[0].[InstanceId,CurrentState.Name]' --output text
    aws ec2 wait instance-terminated --region "$REGION" --instance-ids "$IID" && note "terminated"
  else
    note "no live instance to terminate — going straight to verification"
  fi
  [ -n "$SG" ] && aws ec2 delete-security-group --region "$REGION" --group-id "$SG" 2>&1 | tail -1
  aws ec2 delete-key-pair --region "$REGION" --key-name "$NAME" >/dev/null 2>&1
  rm -f "$STATE/iid" "$STATE/sgid" "$STATE/ip"
  exec "$0" status --region "$REGION" --name "$NAME"
  ;;

status)
  # Verified by re-query, not by the console and not by the exit code of the
  # call that was supposed to do it. Check storage too: an instance going away
  # does not mean its volume did.
  echo "== instances not terminated, $REGION =="
  aws ec2 describe-instances --region "$REGION" \
    --filters Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down \
    --query 'Reservations[].Instances[].[InstanceId,InstanceType,State.Name,Tags[?Key==`Name`].Value|[0]]' \
    --output text | sed 's/^/  /'
  echo "== available (orphaned) volumes =="
  aws ec2 describe-volumes --region "$REGION" --filters Name=status,Values=available \
    --query 'Volumes[].[VolumeId,Size,CreateTime]' --output text | sed 's/^/  /'
  echo "== unassociated elastic IPs =="
  aws ec2 describe-addresses --region "$REGION" \
    --query 'Addresses[?AssociationId==null].[PublicIp,AllocationId]' --output text | sed 's/^/  /'
  echo "== leftover key pair / security group named $NAME =="
  aws ec2 describe-key-pairs --region "$REGION" \
    --query "KeyPairs[?KeyName=='$NAME'].KeyName" --output text | sed 's/^/  /'
  aws ec2 describe-security-groups --region "$REGION" \
    --query "SecurityGroups[?GroupName=='$NAME'].GroupId" --output text | sed 's/^/  /'
  echo "  (blank sections above mean nothing is left)"
  echo "== other regions, in case a session was launched outside $REGION =="
  for r in us-east-1 us-east-2 us-west-1 eu-west-1; do
    [ "$r" = "$REGION" ] && continue
    n="$(aws ec2 describe-instances --region "$r" \
         --filters Name=instance-state-name,Values=pending,running \
         --query 'length(Reservations[].Instances[])' --output text 2>/dev/null || echo '?')"
    echo "  $r: $n running"
  done
  ;;

*)
  die "usage: $0 {up|down|status} [options]   (--help for the rest)";;
esac
