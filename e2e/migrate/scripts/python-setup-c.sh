#!/usr/bin/env bash
# Scenario C: a Python home from another machine. As olduser, the Python lola installs review
# for claude-code into the project /home/olduser/proj and at user scope, and kit, added from the
# folder /home/olduser/src/kit, at user scope; then the home, the project and the folder move to
# user c (/home/c/.lola, /home/c/proj, /home/c/src/kit) and /home/olduser is gone, Claude Code's
# user files with it. The project path and kit's source are missing here, and claude-code is
# neither on c's PATH nor set up at c's user scope; a stand-in opencode is on c's PATH.
set -euo pipefail
export HOME=/home/olduser
mkdir -p /home/olduser/proj /home/olduser/src /home/c/bin
cp -r /e2e/fixtures/kit /home/olduser/src/kit
lola mod add /e2e/fixtures/review
lola mod add /home/olduser/src/kit
lola install review -a claude-code /home/olduser/proj
(cd /home/olduser/proj && lola install review -a claude-code --scope user && lola install kit -a claude-code --scope user)
mv /home/olduser/.lola /home/c/.lola
mv /home/olduser/proj /home/c/proj
mv /home/olduser/src /home/c/src
rm -rf /home/olduser
printf '#!/bin/sh\necho 1.0.0\n' > /home/c/bin/opencode
chmod 755 /home/c/bin/opencode
echo "python lola installed"
