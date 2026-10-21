package scriptaudit

import (
	"path"
	"regexp"
	"strings"
)

var (
	shells = set("sh", "bash", "zsh", "dash", "ash", "ksh", "mksh", "fish", "csh", "tcsh", "python", "python2", "python3", "perl", "ruby", "node", "php", "lua", "pwsh", "powershell", "source", ".", "eval")
	// downloaders and what else brings code or data in from another machine
	downloaders = set("curl", "wget", "fetch", "aria2c", "http", "https", "lwp-request", "nc", "ncat", "netcat", "socat", "telnet", "tftp")
	senders     = set("curl", "wget", "nc", "ncat", "netcat", "socat", "telnet", "ssh", "scp", "sftp", "ftp", "rsync", "tftp", "mail", "mutt", "sendmail", "http", "https")
	decoders    = set("base64", "xxd", "uudecode", "openssl", "gzip", "gunzip", "zcat", "bzcat", "xzcat")
	envDumpers  = set("env", "printenv")
	waitsInput  = set("read", "select", "vi", "vim", "nvim", "nano", "emacs", "less", "more", "man", "top", "htop", "passwd", "ssh", "sftp", "telnet", "ftp")
	powerOff    = set("shutdown", "reboot", "halt", "poweroff", "telinit", "killall5")
	diskTools   = set("mke2fs", "mkswap", "wipefs", "blkdiscard", "sgdisk", "sfdisk", "fdisk", "parted", "gdisk", "lvremove", "vgremove", "pvremove")
	backgrounds = set("nohup", "setsid", "disown", "at", "batch", "crontab")
	// wrappers: run the command that follows them. The value is the options that take an argument of their own.
	wrappers = map[string]string{
		"sudo": "-u -g -h -p -C -D -R -T -U", "doas": "-u -C", "pkexec": "--user", "env": "-u -C -S", "nice": "-n", "ionice": "-c -n -p",
		"nohup": "", "time": "-f -o", "command": "", "builtin": "", "exec": "-a", "stdbuf": "-i -o -e", "timeout": "-s -k", "setsid": "",
		"xargs": "-a -d -E -I -L -n -P -s", "chrt": "", "unbuffer": "",
	}
	// the paths a script must not write to without being looked at: the system, the startup files that every later shell runs, the
	// keys that let someone in, the disks
	sensitivePath = regexp.MustCompile(`^(?:~|\$HOME|\$\{HOME\}|/home/[^/]+|/root|/Users/[^/]+)/(?:\.ssh(?:/|$)|\.bashrc|\.bash_profile|\.bash_login|\.bash_logout|\.profile|\.zshrc|\.zprofile|\.zshenv|\.zlogin|\.config/fish|\.gnupg(?:/|$)|\.aws(?:/|$)|\.netrc|\.git-credentials|\.config/autostart|\.config/systemd|\.local/share/applications|\.pam_environment|\.xinitrc|\.xprofile)` +
		`|^/(?:etc|boot|root|bin|sbin|lib|lib64|usr/bin|usr/sbin|usr/lib|usr/local/bin|usr/local/sbin|var/spool/cron|var/lib/dpkg|var/lib/rpm|sys|proc)(?:/|$)` +
		`|^/dev/(?:sd|nvme|vd|hd|xvd|mmcblk|disk|rdisk|loop|mem|kmem|port|mapper)`)
	// secrets that live in files
	secretFile = regexp.MustCompile(`(?:\.ssh/(?:id_|[^/]*_key|authorized_keys)|\.aws/(?:credentials|config)|\.gnupg/|\.netrc|\.git-credentials|\.config/gh/hosts|\.docker/config\.json|\.kube/config|/etc/shadow|/etc/sudoers|\.npmrc|\.pypirc|\.pgpass|\.vault-token|\.config/rclone|(?:^|/)\.env(?:\.[a-z]+)?$|id_rsa|id_ed25519|/proc/[^/]+/environ)`)
	setuid     = regexp.MustCompile(`^(?:[0-7]?[2467][0-7]{3}|[ugoa]*[+=][rwxXt]*s[rwxXt]*)$`)
	forkBomb   = regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&?\s*\}`)
	netDevice  = regexp.MustCompile(`^/dev/(?:tcp|udp)/`)
	cmdSubDown = regexp.MustCompile(`\b(?:curl|wget|fetch|nc|ncat)\b`)
)

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// command is a stage with the wrappers (sudo, env, nohup...) taken off.
type command struct {
	s        simple
	name     string
	args     []word
	wrappers []string
}

func (c command) texts() []string {
	var t []string
	for _, a := range c.args {
		t = append(t, a.text)
	}
	return t
}

func unwrap(s simple) (command, bool) {
	ws := s.words
	// leading assignments (the rough reading leaves them in)
	for len(ws) > 0 && regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`).MatchString(ws[0].text) {
		ws = ws[1:]
	}
	c := command{s: s}
	for len(ws) > 0 {
		name := path.Base(ws[0].text)
		takes, isWrapper := wrappers[name]
		if !isWrapper {
			c.name, c.args = name, ws[1:]
			return c, true
		}
		c.wrappers = append(c.wrappers, name)
		ws = ws[1:]
		for len(ws) > 0 {
			t := ws[0].text
			switch {
			case strings.HasPrefix(t, "-") && t != "-":
				ws = ws[1:]
				if hasWord(takes, t) && len(ws) > 0 {
					ws = ws[1:]
				}
			case name == "env" && strings.Contains(t, "="):
				ws = ws[1:]
			case name == "timeout" && len(ws) > 0 && regexp.MustCompile(`^\d`).MatchString(t) && !c.hasPositional(name):
				c.wrappers[len(c.wrappers)-1] += ":"
				ws = ws[1:]
			default:
				goto next
			}
		}
	next:
	}
	if len(c.wrappers) > 0 {
		// nothing after the wrapper: env, time... on their own are the command
		c.name = strings.TrimSuffix(c.wrappers[len(c.wrappers)-1], ":")
		c.wrappers = c.wrappers[:len(c.wrappers)-1]
		return c, true
	}
	return c, false
}

func (c command) hasPositional(string) bool {
	return strings.HasSuffix(c.wrappers[len(c.wrappers)-1], ":")
}

func hasWord(list, w string) bool {
	for _, x := range strings.Fields(list) {
		if x == w {
			return true
		}
	}
	return false
}

// flags are the single-letter options (-rf is r and f) and the long ones (--force) among args.
func flags(args []word) (short string, long map[string]bool) {
	long = map[string]bool{}
	for _, a := range args {
		switch {
		case strings.HasPrefix(a.text, "--"):
			long[strings.SplitN(a.text, "=", 2)[0]] = true
		case strings.HasPrefix(a.text, "-") && len(a.text) > 1:
			short += a.text[1:]
		}
	}
	return
}

func operands(args []word) []word {
	var out []word
	for _, a := range args {
		if !strings.HasPrefix(a.text, "-") || a.text == "-" {
			out = append(out, a)
		}
	}
	return out
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 140 {
		return string(r[:140]) + "…"
	}
	return s
}

// dangerousTarget says why a path given to a recursive remove or change must not be taken on trust.
func dangerousTarget(a word) (string, bool) {
	t := strings.TrimRight(a.text, "/")
	switch t {
	case "", "~", "$HOME", "${HOME}", ".", "..", "*", "./*", "../*", "~/*", "$HOME/*", "${HOME}/*", "/*":
		return "the root, the home directory or the current one (" + a.text + ")", true
	}
	if sensitivePath.MatchString(a.text) || regexp.MustCompile(`^/(?:usr|var|opt|srv|mnt|media|run|home|lib)(?:/|$)`).MatchString(a.text) {
		return "a system path (" + a.text + ")", true
	}
	if !a.lit {
		return "a path from a variable or a command (" + a.text + "), which can be empty", true
	}
	return "", false
}

// checkPipeline applies the rules to a pipeline.
func checkPipeline(p pipeline) []Finding {
	var out []Finding
	whole := func(sev Severity, rule, msg string) {
		var texts []string
		for _, s := range p.stages {
			texts = append(texts, s.text)
		}
		out = append(out, Finding{Line: p.line, Severity: sev, Rule: rule, Message: msg, Text: clip(strings.Join(texts, " | "))})
	}
	add := func(s simple, sev Severity, rule, msg string) {
		out = append(out, Finding{Line: s.line, Severity: sev, Rule: rule, Message: msg, Text: clip(s.text)})
	}
	var cmds []command
	for _, s := range p.stages {
		c, ok := unwrap(s)
		if !ok {
			// only redirects and assignments, or nothing but wrappers
			checkRedirects(s, add)
			if len(c.wrappers) > 0 && c.name == "" {
				continue
			}
			continue
		}
		cmds = append(cmds, c)
	}
	for _, s := range p.stages {
		if s.background {
			add(s, Warn, "background", "runs in the background: it may still be running when the recording ends")
		}
		checkRedirects(s, add)
	}

	// across the pipeline
	for i, c := range cmds {
		for _, sub := range cmds[i+1:] {
			switch {
			case downloaders[c.name] && shells[sub.name]:
				whole(Block, "download-and-run", "runs what "+c.name+" downloads, without it having been looked at")
			case decoders[c.name] && shells[sub.name] && decodes(c):
				whole(Block, "decode-and-run", "decodes something and runs it: what it runs cannot be read in the script")
			}
		}
	}
	secrets, sender := false, false
	for _, c := range cmds {
		if envDumpers[c.name] && len(operands(c.args)) == 0 {
			secrets = true
		}
		for _, a := range c.args {
			if secretFile.MatchString(a.text) {
				secrets = true
			}
		}
		for _, r := range c.s.redirs {
			if r.op == "<" && secretFile.MatchString(r.target.text) {
				secrets = true
			}
		}
		if senders[c.name] {
			sender = true
		}
	}
	if secrets && sender {
		whole(Block, "sends-secrets", "reads a secret (a key, a credential, the environment) and has a command that sends data to another machine")
	}

	for _, c := range cmds {
		out = append(out, checkCommand(c, add)...)
	}
	return out
}

func decodes(c command) bool {
	short, long := flags(c.args)
	switch c.name {
	case "base64":
		return strings.ContainsAny(short, "dD") || long["--decode"]
	case "xxd":
		return strings.Contains(short, "r")
	case "openssl":
		return len(c.args) > 0 && c.args[0].text == "enc" && strings.Contains(short, "d")
	case "gzip":
		return strings.Contains(short, "d") || long["--decompress"]
	}
	return true // uudecode, gunzip, zcat...
}

func checkRedirects(s simple, add func(simple, Severity, string, string)) {
	for _, r := range s.redirs {
		t := r.target.text
		if netDevice.MatchString(t) {
			add(s, Block, "network-device", "opens a network connection through the shell ("+t+"): a way to a reverse shell")
			continue
		}
		switch r.op {
		case ">", ">>", ">|", "&>", "&>>", "<>", ">&":
			if sensitivePath.MatchString(t) {
				add(s, Block, "system-file", "writes to a system or startup file, a key or a disk ("+t+")")
			}
		}
	}
}

func checkCommand(c command, add func(simple, Severity, string, string)) []Finding {
	var extra []Finding
	short, long := flags(c.args)
	ops := operands(c.args)
	s := c.s
	texts := c.texts()
	has := func(f string) bool { return strings.Contains(short, f) }

	for _, w := range c.wrappers {
		switch strings.TrimSuffix(w, ":") {
		case "sudo", "doas", "pkexec":
			add(s, Warn, "privilege", "runs as another user (root), and asks for a password unless it is set up not to")
		case "nohup", "setsid":
			add(s, Warn, "background", "is kept running after the shell ends")
		}
	}
	if waitsInput[c.name] {
		add(s, Warn, "waits-for-input", c.name+" waits for a person: nothing is typed into it, so the session waits until it ends")
	}
	if c.name == "su" {
		add(s, Warn, "privilege", "su asks for a password")
	}
	if backgrounds[c.name] {
		rule, msg := "background", c.name+" leaves something running or scheduled after the recording"
		if c.name == "crontab" && has("r") {
			add(s, Block, "crontab", "crontab -r removes every scheduled job")
		} else {
			add(s, Warn, rule, msg)
		}
	}
	if powerOff[c.name] || (c.name == "init" && len(ops) == 1 && (ops[0].text == "0" || ops[0].text == "6")) {
		add(s, Block, "power", c.name+" takes the machine down")
	}
	if c.name == "systemctl" && len(ops) > 0 {
		switch ops[0].text {
		case "poweroff", "reboot", "halt", "kexec", "suspend", "hibernate", "rescue", "emergency", "isolate":
			add(s, Block, "power", "systemctl "+ops[0].text+" changes the state of the whole machine")
		case "enable", "mask", "disable", "start", "stop", "restart", "daemon-reload":
			add(s, Warn, "service", "changes a service")
		}
	}
	if c.name == "kill" || c.name == "pkill" || c.name == "killall" {
		for _, o := range ops {
			if o.text == "-1" || o.text == "1" || o.text == "0" {
				add(s, Block, "kill-all", c.name+" "+o.text+" signals every process, or init")
			}
		}
		if c.name != "kill" {
			add(s, Warn, "kill", "kills processes by name: more than you meant may match")
		}
		if has("1") && c.name == "kill" {
			add(s, Block, "kill-all", "kill -1 hangs up on every process")
		}
	}
	switch {
	case c.name == "rm" || c.name == "shred" || c.name == "unlink" || c.name == "rmdir":
		recursive := strings.ContainsAny(short, "rR") || long["--recursive"]
		if long["--no-preserve-root"] {
			add(s, Block, "rm-root", "--no-preserve-root removes the safeguard that stops rm / ")
		}
		for _, o := range ops {
			if why, bad := dangerousTarget(o); bad && recursive {
				add(s, Block, "rm-recursive", "removes recursively: "+why)
			} else if sensitivePath.MatchString(o.text) {
				add(s, Block, "system-file", "removes a system or startup file, a key or a disk ("+o.text+")")
			}
		}
	case strings.HasPrefix(c.name, "mkfs") || diskTools[c.name]:
		add(s, Block, "disk", c.name+" changes or erases a disk")
	case c.name == "cryptsetup" && len(ops) > 0 && (ops[0].text == "luksFormat" || ops[0].text == "erase" || ops[0].text == "luksErase"):
		add(s, Block, "disk", "cryptsetup "+ops[0].text+" destroys an encrypted volume")
	case c.name == "dd":
		for _, t := range texts {
			if strings.HasPrefix(t, "of=") && sensitivePath.MatchString(strings.TrimPrefix(t, "of=")) {
				add(s, Block, "disk-write", "dd writes to "+strings.TrimPrefix(t, "of="))
			}
		}
	case c.name == "chmod" || c.name == "chown" || c.name == "chgrp":
		recursive := strings.ContainsAny(short, "R") || long["--recursive"]
		for i, o := range ops {
			if c.name == "chmod" && i == 0 && setuid.MatchString(o.text) {
				add(s, Block, "setuid", "sets the setuid or setgid bit ("+o.text+"): a program that always runs as its owner")
			}
			if i == 0 && c.name != "chmod" || i > 0 || (c.name == "chmod" && i == 0 && false) {
				if why, bad := dangerousTarget(o); bad && recursive {
					add(s, Block, "chmod-recursive", c.name+" -R on "+why)
				} else if sensitivePath.MatchString(o.text) {
					add(s, Block, "system-file", "changes a system or startup file, a key or a disk ("+o.text+")")
				}
			}
		}
		if c.name == "chmod" && recursive {
			add(s, Warn, "chmod-recursive", "changes permissions recursively")
		}
	case c.name == "tee" || c.name == "truncate" || c.name == "touch" || c.name == "chattr" || c.name == "install" || c.name == "cp" || c.name == "mv" || c.name == "ln" || c.name == "rsync" || (c.name == "sed" && (has("i") || long["--in-place"])):
		dest := ops
		if (c.name == "cp" || c.name == "install" || c.name == "rsync" || c.name == "ln") && len(ops) > 0 {
			dest = ops[len(ops)-1:]
		}
		for _, o := range dest {
			if sensitivePath.MatchString(o.text) {
				add(s, Block, "system-file", c.name+" writes to a system or startup file, a key or a disk ("+o.text+")")
			}
		}
		if c.name == "rsync" || c.name == "scp" {
			remoteCopy(c, add)
		}
	case c.name == "scp" || c.name == "sftp":
		remoteCopy(c, add)
	case c.name == "docker" || c.name == "podman":
		for _, t := range texts {
			if t == "--privileged" {
				add(s, Warn, "container", "a privileged container can do to the host what root can")
			}
			if regexp.MustCompile(`(?:^|=)/:`).MatchString(t) || t == "/:/host" {
				add(s, Block, "container", "gives a container the whole file system of the host")
			}
		}
	case c.name == "git":
		if len(ops) > 0 {
			switch ops[0].text {
			case "push":
				if has("f") || long["--force"] || long["--force-with-lease"] || long["--mirror"] || long["--delete"] {
					add(s, Warn, "git", "git push --force replaces history on the remote")
				}
			case "reset":
				if long["--hard"] {
					add(s, Warn, "git", "git reset --hard throws away changes that are not committed")
				}
			case "clean":
				if strings.Contains(short, "f") || long["--force"] {
					add(s, Warn, "git", "git clean -f deletes the files that git does not know")
				}
			}
		}
	case c.name == "find":
		for _, t := range texts {
			if t == "-delete" {
				add(s, Warn, "find-delete", "find -delete deletes what it finds")
			}
		}
	case c.name == "eval":
		add(s, Warn, "eval", "eval runs text that is made when the script runs: what it does cannot be read from the script")
	}

	// a shell given text to run: read that text too
	if shells[c.name] {
		for i, t := range texts {
			if (t == "-c" || (strings.HasPrefix(t, "-") && strings.HasSuffix(t, "c") && !strings.HasPrefix(t, "--"))) && i+1 < len(c.args) {
				arg := c.args[i+1]
				if len(arg.subs) > 0 && cmdSubDown.MatchString(strings.Join(arg.subs, " ")) {
					add(s, Block, "download-and-run", "runs what is downloaded by a command inside it, without it having been looked at")
				}
				if arg.lit {
					for _, f := range Audit(arg.text, Bash) {
						f.Line, f.Text = s.line, clip(s.text)
						f.Message = "(inside " + c.name + " -c) " + f.Message
						extra = append(extra, f)
					}
				} else if arg.text != "" {
					add(s, Warn, "eval", c.name+" -c runs text that is made when the script runs")
				}
			}
		}
	}
	// code from another machine through a substitution: bash <(curl ...), source <(...), eval "$(curl ...)"
	if shells[c.name] {
		for _, a := range c.args {
			if len(a.subs) > 0 && cmdSubDown.MatchString(strings.Join(a.subs, " ")) {
				add(s, Block, "download-and-run", "runs what is downloaded by a command inside it, without it having been looked at")
			}
		}
	}
	// data to another machine
	if downloaders[c.name] || c.name == "curl" || c.name == "wget" {
		upload := long["--data"] || long["--data-binary"] || long["--data-raw"] || long["--data-urlencode"] || long["--form"] || long["--upload-file"] || long["--post-file"] || long["--post-data"] || long["--json"] ||
			strings.ContainsAny(short, "dFT")
		for i, t := range texts {
			if (t == "-X" || t == "--request") && i+1 < len(texts) {
				switch strings.ToUpper(texts[i+1]) {
				case "POST", "PUT", "PATCH", "DELETE":
					upload = true
				}
			}
		}
		if upload && (c.name == "curl" || c.name == "wget") {
			add(s, Warn, "upload", c.name+" sends data to another machine")
		}
	}
	if c.name == "nc" || c.name == "ncat" || c.name == "netcat" || c.name == "socat" {
		if has("e") || has("c") || long["--exec"] || long["--sh-exec"] || long["--lua-exec"] {
			add(s, Block, "reverse-shell", c.name+" -e hands a shell to the other end of a connection")
		} else {
			add(s, Warn, "network", c.name+" opens a network connection")
		}
	}
	for _, t := range texts {
		if secretFile.MatchString(t) && !senders[c.name] {
			add(s, Warn, "secret", "reads a secret ("+t+"): it will be in the recording")
		}
	}
	return extra
}

func remoteCopy(c command, add func(simple, Severity, string, string)) {
	for _, o := range operands(c.args) {
		if strings.Contains(o.text, ":") && !strings.HasPrefix(o.text, "/") && !strings.HasPrefix(o.text, "./") {
			add(c.s, Warn, "remote-copy", c.name+" copies to or from another machine")
			return
		}
	}
}
