include ../../../Makefile.variables
include ../../../Makefile.env
include ../../../Makefile.k8s

# VAULTS tuple: '<vault-name>=<gateway-secret-teamvault-id>=<task-dir>'
# Stage-split: tvids differ per environment. Pattern mirrors trading/mt5/connector/k8s/Makefile.
#
# task-dir is the directory inside the vault repo where this controller
# materializes task files. The convention differs per vault:
#   openclaw → tasks         (OpenClaw vault convention)
#   personal → 24 Tasks      (Personal vault convention; matches Obsidian
#                             folder pattern "24 Tasks/" for 1-7-day work)
# The value is passed to the controller via TASK_DIR env (overriding the
# binary default "24 Tasks").
#
# MIGRATION NOTE (one-time, when this PR first deploys):
# The legacy openclaw STS is named `agent-task-controller` (no -openclaw suffix).
# Applying this Makefile creates `agent-task-controller-openclaw` as a NEW STS;
# the old STS + PVC must be deleted manually after the new one is Ready:
#
#   kubectlquant -n <ns> delete sts agent-task-controller
#   kubectlquant -n <ns> delete pvc datadir-agent-task-controller-0
#
# Bolt data loss (Kafka consumer offsets + dedup) is recoverable: Kafka replays
# from offset 0; idempotent UUID5 task identifiers prevent duplicate vault files.
ifeq ($(BRANCH),prod)
	VAULTS ?= \
		'openclaw=3OlKKw=tasks' \
		'personal=dwkkgw=24 Tasks'
else ifeq ($(BRANCH),dev)
	VAULTS ?= \
		'openclaw=YLbzyL=tasks' \
		'personal=9L6eMO=24 Tasks'
else
	$(error apply requires BRANCH=dev or BRANCH=prod)
endif

apply:
	@for i in $(VAULTS); do \
		export VAULT="$$(echo $$i | cut -d'=' -f1)"; \
		export GATEWAY_SECRET_TVID="$$(echo $$i | cut -d'=' -f2)"; \
		export TASK_DIR="$$(echo $$i | cut -d'=' -f3-)"; \
		echo "applying for vault $${VAULT} (task-dir=$${TASK_DIR})"; \
		$(MAKE) applyOne; \
	done

applyOne:
	@if [ "${BRANCH}" = "prod" ]; then \
		bash -c 'source ${ROOTDIR}/prod.env && source ${ROOTDIR}/common.env && find . -name "*.yaml" | while read -r file; do cat $${file} | teamvault-config-parser -teamvault-config="${TEAMVAULT}" -logtostderr -v=0 | kubectlquant apply -f -; done'; \
	elif [ "${BRANCH}" = "dev" ]; then \
		bash -c 'source ${ROOTDIR}/dev.env && source ${ROOTDIR}/common.env && find . -name "*.yaml" | while read -r file; do cat $${file} | teamvault-config-parser -teamvault-config="${TEAMVAULT}" -logtostderr -v=0 | kubectlquant apply -f -; done'; \
	else \
		echo 'apply only allowed in prod or dev branch!' >&2; \
		exit 1; \
	fi
