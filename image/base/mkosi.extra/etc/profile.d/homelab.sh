# nano edits (git commit, systemctl edit) unless the user's own profile
# says otherwise.
export EDITOR="${EDITOR:-nano}" VISUAL="${VISUAL:-nano}"

# The homelab-ngx shell: a greeting at login and a two-line prompt, for
# interactive bash (usr/share/homelab/shell.bash). A plain sh stops here.
[ -n "$BASH_VERSION" ] && [ -n "$PS1" ] && [ -r /usr/share/homelab/shell.bash ] && . /usr/share/homelab/shell.bash
