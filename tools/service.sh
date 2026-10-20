#!/bin/zsh
# Install a per-user launchd service, independent of the source checkout.
set -eu
umask 077

project_dir="${0:A:h:h}"
label="local.xerox-airscan"
domain="gui/$(id -u)"
service="$domain/$label"
app_dir="$HOME/Library/Application Support/Xerox AirScan"
app_bundle="$app_dir/Xerox AirScan.app"
log_dir="$HOME/Library/Logs/Xerox AirScan"
plist="$HOME/Library/LaunchAgents/$label.plist"

unload() {
  if /bin/launchctl print "$service" >/dev/null 2>&1; then
    /bin/launchctl bootout "$service"
  fi
}

case "${1:-status}" in
  install)
    export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
    make -C "$project_dir" GO='asdf exec go' build
    if [[ ! -f "$app_dir/config.json" && ! -f "$project_dir/config.json" ]]; then
      print -u2 'Create config.json from config.example.json and set scanner_address first.'
      exit 1
    fi
    # Prepare the complete plist before interrupting an installed service.
    staged_plist="$(mktemp -t xerox-airscan)"
    trap 'rm -f "$staged_plist"' EXIT
    /usr/bin/plutil -create xml1 "$staged_plist"
    /usr/bin/plutil -insert Label -string "$label" "$staged_plist"
    /usr/bin/plutil -insert ProgramArguments -array "$staged_plist"
    /usr/bin/plutil -insert ProgramArguments.0 -string "$app_bundle/Contents/MacOS/xerox-airscan" "$staged_plist"
    /usr/bin/plutil -insert ProgramArguments.1 -string serve "$staged_plist"
    /usr/bin/plutil -insert ProgramArguments.2 -string --config "$staged_plist"
    /usr/bin/plutil -insert ProgramArguments.3 -string "$app_dir/config.json" "$staged_plist"
    /usr/bin/plutil -insert ProgramArguments.4 -string --retry-startup "$staged_plist"
    /usr/bin/plutil -insert AssociatedBundleIdentifiers -array "$staged_plist"
    /usr/bin/plutil -insert AssociatedBundleIdentifiers.0 -string "$label" "$staged_plist"
    /usr/bin/plutil -insert WorkingDirectory -string "$app_dir" "$staged_plist"
    /usr/bin/plutil -insert RunAtLoad -bool true "$staged_plist"
    /usr/bin/plutil -insert KeepAlive -bool true "$staged_plist"
    /usr/bin/plutil -insert ThrottleInterval -integer 30 "$staged_plist"
    /usr/bin/plutil -insert ExitTimeOut -integer 10 "$staged_plist"
    /usr/bin/plutil -insert Umask -integer 63 "$staged_plist"
    /usr/bin/plutil -insert StandardOutPath -string "$log_dir/stdout.log" "$staged_plist"
    /usr/bin/plutil -insert StandardErrorPath -string "$log_dir/stderr.log" "$staged_plist"
    /usr/bin/plutil -lint "$staged_plist"

    mkdir -p "$app_bundle/Contents/MacOS" "$log_dir" "${plist:h}"
    unload
    /usr/bin/install -m 700 "$project_dir/bin/xerox-airscan" "$app_bundle/Contents/MacOS/xerox-airscan"
    /usr/bin/install -m 600 "$project_dir/tools/service-info.plist" "$app_bundle/Contents/Info.plist"
    /usr/bin/codesign --force --sign - "$app_bundle"
    /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$app_bundle"
    # Preserve settings edited in the installed copy on subsequent installs.
    if [[ ! -f "$app_dir/config.json" ]]; then
      /usr/bin/install -m 600 "$project_dir/config.json" "$app_dir/config.json"
    fi
    /usr/bin/install -m 600 "$staged_plist" "$plist"
    /bin/launchctl enable "$service"
    /bin/launchctl bootstrap "$domain" "$plist"
    print "Installed $label; it starts at login and restarts automatically."
    print "Configuration: $app_dir/config.json"
    print "Logs: $log_dir"
    ;;
  status)
    /bin/launchctl print "$service"
    ;;
  restart)
    # SIGTERM lets an active scanner session close before launchd restarts it.
    unload
    /bin/launchctl bootstrap "$domain" "$plist"
    ;;
  stop)
    /bin/launchctl disable "$service"
    unload
    ;;
  start)
    /bin/launchctl enable "$service"
    if ! /bin/launchctl print "$service" >/dev/null 2>&1; then
      /bin/launchctl bootstrap "$domain" "$plist"
    fi
    ;;
  uninstall)
    unload
    rm -f "$plist"
    print "Removed login service. Configuration, binary, and logs remain in your Library."
    ;;
  *)
    print -u2 "Usage: $0 {install|status|restart|stop|start|uninstall}"
    exit 2
    ;;
esac
