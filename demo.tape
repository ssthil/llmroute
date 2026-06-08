# llmroute terminal demo — render with: vhs demo.tape  (or: make demo)
# Produces docs/demo.gif. Self-contained: no API keys or live model needed.
# https://github.com/charmbracelet/vhs

Output docs/demo.gif
Require llmroute

Set Shell "bash"
Set FontSize 18
Set Width 1120
Set Height 760
Set Padding 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 55ms

# Use a throwaway config dir so the demo is reproducible and never touches
# real state, and make sure the installed binary is on PATH.
Hide
Type "export XDG_CONFIG_HOME=$(mktemp -d); export PATH=$HOME/.local/bin:$PATH; clear"
Enter
Show

# First run — the welcome screen.
Type "llmroute"
Enter
Sleep 3s

# Set up the catalog (non-interactive for the demo).
Type "llmroute init --yes"
Enter
Sleep 3s

# Add a local, no-key model (Ollama).
Type "llmroute models add --provider ollama --base-url http://127.0.0.1:11434/v1/chat/completions --id gemma3:27b --intents chat,code --no-key"
Enter
Sleep 2500ms

# Status view — banner + catalog with the local model enabled.
Type "llmroute"
Enter
Sleep 3500ms
