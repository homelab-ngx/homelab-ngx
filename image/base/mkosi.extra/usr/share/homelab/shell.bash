# The shell's look on a homelab-ngx host, read by /etc/profile.d/homelab.sh:
# a short greeting at login and a two-line prompt in the dashboard's
# colours. Nerd Font icons on the console (kmscon's font has them) or where
# HOMELAB_ICONS=1 (an SSH client with a Nerd Font); plain characters where
# the font may not have them. The icons are literal UTF-8: bash expands
# $'\U...' only in a UTF-8 locale, which profile.d sets after this file.

# History from every SSH session, appended rather than overwritten.
HISTSIZE=10000 HISTFILESIZE=20000
shopt -s histappend

_hl_icons=
[[ $TERM == kmscon || $HOMELAB_ICONS == 1 ]] && _hl_icons=1

# The dashboard's colours, as r;g;b.
_hl_blue='90;169;230' _hl_ink='16;19;23' _hl_slate='42;48;56' _hl_text='216;221;227'
_hl_dim='92;102;114' _hl_red='229;112;122' _hl_green='124;199;154'

# _hl_c FG [BG]: a colour change for PS1, marked as taking no space.
_hl_c() {
  local s="\[\e[0;38;2;$1m"
  [[ -n ${2:-} ]] && s+="\e[48;2;$2m"
  printf '%s\]' "$s"
}

_hl_prompt() {
  local code=$? branch p reset='\[\e[0m\]'
  branch=$(git symbolic-ref --short -q HEAD 2>/dev/null)
  if [[ -n $_hl_icons ]]; then
    # Pills: host, path, branch, a failed command's exit code.
    p="$(_hl_c "$_hl_dim")╭─$(_hl_c "$_hl_blue")"$''
    p+="$(_hl_c "$_hl_ink" "$_hl_blue")"'󰒋'" \h $(_hl_c "$_hl_blue" "$_hl_slate")"$''
    p+="$(_hl_c "$_hl_text" "$_hl_slate") "$''" \w "
    local last=$_hl_slate
    if [[ -n $branch ]]; then
      p+="$(_hl_c "$_hl_slate" "$_hl_green")"$''"$(_hl_c "$_hl_ink" "$_hl_green") "$''" $branch "
      last=$_hl_green
    fi
    if ((code)); then
      p+="$(_hl_c "$last" "$_hl_red")"$''"$(_hl_c "$_hl_ink" "$_hl_red") "'󰅖'" $code "
      last=$_hl_red
    fi
    p+="$(_hl_c "$last")"$''"$reset\n$(_hl_c "$_hl_dim")╰─"
  else
    p="$(_hl_c "$_hl_dim")┌─$(_hl_c "$_hl_ink" "$_hl_blue") \h $(_hl_c "$_hl_text" "$_hl_slate") \w "
    [[ -n $branch ]] && p+="$(_hl_c "$_hl_ink" "$_hl_green") $branch "
    ((code)) && p+="$(_hl_c "$_hl_ink" "$_hl_red") ! $code "
    p+="$reset\n$(_hl_c "$_hl_dim")└─"
  fi
  if ((code)); then p+="$(_hl_c "$_hl_red")"; else p+="$(_hl_c "$_hl_blue")"; fi
  PS1="$p❯$reset "
}
PROMPT_COMMAND=(_hl_prompt "${PROMPT_COMMAND[@]}")

# _hl_line ICON TEXT: one line of the greeting.
_hl_line() {
  if [[ -n $_hl_icons ]]; then
    printf '  \e[38;2;%sm%s\e[0m  %s\n' "$_hl_blue" "$1" "$2"
  else
    printf '  %s\n' "$2"
  fi
}

# _hl_versions: the image's version, or the image's and the core's when they
# differ (an update half done), then the daemon's on a line of its own when
# it still runs another core. The daemon is asked only by a user who may
# open its socket, and for at most a second.
_hl_versions() {
  local IMAGE_VERSION='' core daemon sock=/run/homelab-daemon/daemon.sock
  # shellcheck disable=SC1091
  . /etc/os-release
  core=$(readlink /var/lib/extensions/homelab-core.raw 2>/dev/null)
  [[ $core =~ homelab-core_(.+)\.raw$ ]] && core=${BASH_REMATCH[1]} || core=
  if [[ -z $core || $core == "$IMAGE_VERSION" ]]; then
    _hl_v="homelab-ngx ${IMAGE_VERSION:-}"
  else
    _hl_v="image $IMAGE_VERSION · core $core"
  fi
  _hl_d=
  if [[ -n $core && -w $sock ]]; then
    daemon=$(curl -s --max-time 1 --unix-socket "$sock" http://localhost/api/version 2>/dev/null |
      sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
    [[ -n $daemon && $daemon != "$core" ]] && _hl_d="daemon $daemon: it has not restarted into core $core"
  fi
}

_hl_greet() {
  local host up load mem addr dash m storage='' _hl_v _hl_d
  host=$(</proc/sys/kernel/hostname)
  read -r up _ </proc/uptime
  up=${up%.*}
  if ((up >= 86400)); then up="$((up / 86400))d $((up % 86400 / 3600))h"
  elif ((up >= 3600)); then up="$((up / 3600))h $((up % 3600 / 60))m"
  else up="$((up / 60))m"; fi
  read -r load _ </proc/loadavg
  mem=$(free -h 2>/dev/null | awk '/^Mem/ {print $3 " of " $2}')
  addr=$(ip -4 -o route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
  _hl_versions
  echo
  _hl_line '󰒋' "$(printf '\e[1m%s\e[0m · %s' "$host" "$_hl_v")"
  [[ -n $_hl_d ]] && _hl_line '󰀦' "$_hl_d"
  _hl_line '󰅐' "up $up · load $load · memory $mem"
  # Pools and disks are mounted under /var/mnt, each at its name.
  for m in /var/mnt/*/; do
    m=${m%/}
    mountpoint -q "$m" 2>/dev/null || continue
    storage+="${storage:+ · }$(df -h --output=used,size "$m" | awk -v n="${m##*/}" 'NR == 2 {print n " " $1 " of " $2}')"
  done
  [[ -n $storage ]] && _hl_line '󰋊' "$storage"
  if [[ -e /var/lib/homelab/installer ]]; then
    _hl_line '󰇚' "installer: open http://$host.local${addr:+ or http://$addr} in a browser"
    # The code that moves the installer to another browser (the daemon
    # writes it at its start), and the address to scan with a phone.
    local code=
    [[ -r /run/homelab-installer/takeover-code ]] && code=$(</run/homelab-installer/takeover-code)
    [[ -n $code ]] && _hl_line '󰌆' "take-over code: $code"
    if [[ -n $addr ]] && command -v qrencode >/dev/null; then
      echo
      qrencode -t UTF8 -m 2 "http://$addr" | sed 's/^/  /'
    fi
  elif [[ -r /etc/homelab/dashboard ]] && read -r dash </etc/homelab/dashboard && [[ -n $dash ]]; then
    # The daemon writes it when a private domain is set; plain HTTP to the
    # host then redirects there.
    _hl_line '󰖟' "dashboard: $dash"
  else
    _hl_line '󰖟' "dashboard: http://$host.local${addr:+ · http://$addr}"
  fi
  echo
}
shopt -q login_shell && _hl_greet
