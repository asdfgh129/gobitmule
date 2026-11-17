#!/bin/bash
# lab_ssh.sh — SSH to lab host A with bounded retries.
# Host A's sshd intermittently answers "Exceeded MaxStartups" / resets the
# banner, so a single attempt is not reliable. Usage: lab_ssh.sh "<remote cmd>"
host="${UD_LAB_HOST:-192.168.34.222}"
key="$HOME/.ssh/id_ed25519_udlab"
for i in $(seq 1 15); do
  out=$(ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o LogLevel=ERROR \
        -o ConnectTimeout=10 -i "$key" root@"$host" "$1" 2>&1)
  rc=$?
  # 255 is ssh's own transport failure (host A's sshd intermittently answers
  # "Exceeded MaxStartups" and closes with no message when LogLevel=ERROR), so
  # it must be retried. Any other code is the remote command's real result.
  if [ "$rc" -eq 255 ]; then
    sleep 4
    continue
  fi
  printf '%s\n' "$out"
  exit $rc
done
echo "lab_ssh: host $host unreachable after 15 attempts" >&2
exit 255
