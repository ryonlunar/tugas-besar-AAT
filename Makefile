PYTHON ?= python3
KUBECONFIG ?= $(HOME)/.kube/aat-dev.yaml
export KUBECONFIG
TESTER := kubectl --context aat-dev -n tester-agent
TESTER_MANIFEST := https://raw.githubusercontent.com/susTuna/if4031-tester-agent/ebb55261711123a9a211739ffbf140a275caa6da/manifest.yaml

.PHONY: help up start deploy stop join-worker cluster-check foundation-check pipeline-check pipeline-recovery coastal-check field-alerts-check tester-install tester-list tester-run tester-logs

help:
	@printf '%s\n' \
	  'Host: make up | start | deploy | stop | join-worker NODE=aat-worker-2' \
	  'Control plane: make cluster-check | foundation-check | pipeline-check | pipeline-recovery | coastal-check' \
	  'Control plane: make field-alerts-check EXPECTED=path/to/expected-critical.json' \
	  'Tester Agent: make tester-install | tester-list | tester-logs' \
	  'Tester Agent: make tester-run SCENARIO=kill-leader'

up start deploy stop:
	$(PYTHON) scripts/m2/multipass-cluster.py $@

join-worker:
	$(PYTHON) scripts/m2/multipass-cluster.py $@ "$(NODE)"

cluster-check:
	$(PYTHON) scripts/m2/check-cluster.py

foundation-check:
	$(PYTHON) scripts/m2/check-foundation.py

pipeline-check:
	$(PYTHON) scripts/m2/check-pipeline.py

pipeline-recovery:
	$(PYTHON) scripts/m2/check-pipeline.py --exercise-recovery

coastal-check:
	$(PYTHON) scripts/m2/check-coastal.py

field-alerts-check:
	@test -n "$(EXPECTED)" || { echo 'Usage: make field-alerts-check EXPECTED=path/to/expected-critical.json' >&2; exit 2; }
	$(PYTHON) scripts/m2/check-field-alerts.py "$(EXPECTED)"

tester-install:
	$(PYTHON) scripts/m2/configure-tester-access.py
	kubectl --context aat-dev apply -f $(TESTER_MANIFEST)
	$(TESTER) rollout status daemonset/tester-agent --timeout=180s
	$(TESTER) get pods -l app=tester-agent -o wide

tester-list:
	$(TESTER) exec daemonset/tester-agent -- /app list

tester-run:
	@test -n "$(SCENARIO)" || { echo 'Usage: make tester-run SCENARIO=kill-leader' >&2; exit 2; }
	$(TESTER) exec daemonset/tester-agent -- /app run practice "$(SCENARIO)"

tester-logs:
	$(TESTER) logs -l app=tester-agent --prefix --timestamps --tail=-1
