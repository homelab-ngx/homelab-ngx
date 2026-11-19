# The shell's look on a homelab-ngx host, read by /etc/profile.d/homelab.sh:
# a short greeting at login and a two-line prompt in the dashboard's
# colours. Nerd Font icons on the console (kmscon's font has them) or where
# HOMELAB_ICONS=1 (an SSH client with a Nerd Font); plain characters where
# the font may not have them. The icons are literal UTF-8: bash expands
# $'\U...' only in a UTF-8 locale, which profile.d sets after this file.

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

_hl_greet() {
  local IMAGE_VERSION= host up load mem addr pool
  # shellcheck disable=SC1091
  . /etc/os-release
  host=$(</proc/sys/kernel/hostname)
  read -r up _ </proc/uptime
  up=${up%.*}
  if ((up >= 86400)); then up="$((up / 86400))d $((up % 86400 / 3600))h"
  elif ((up >= 3600)); then up="$((up / 3600))h $((up % 3600 / 60))m"
  else up="$((up / 60))m"; fi
  read -r load _ </proc/loadavg
  mem=$(free -h 2>/dev/null | awk '/^Mem/ {print $3 " of " $2}')
  addr=$(ip -4 -o route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
  echo
  _hl_line '󰒋' "$(printf '\e[1m%s\e[0m · homelab-ngx %s' "$host" "${IMAGE_VERSION:-}")"
  _hl_line '󰅐' "up $up · load $load · memory $mem"
  if mountpoint -q /pool 2>/dev/null; then
    pool=$(df -h --output=used,size /pool | awk 'NR == 2 {print $1 " of " $2}')
    _hl_line '󰋊' "pool $pool"
  fi
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
  else
    _hl_line '󰖟' "dashboard: http://$host.local${addr:+ · http://$addr}"
  fi
  echo
}
shopt -q login_shell && _hl_greet
