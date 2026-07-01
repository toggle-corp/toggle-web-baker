# images/Makefile -- build the platform container images.
#
# Each image has a `docker build` target. Base images are pinned by digest IN
# the Dockerfiles (the trusted, audited reference); the digests are also
# recorded below so a bump is a single, reviewable diff.
#
#   make build           # build all five images
#   make clone           # build one image
#   make test            # run the copier gate unit tests (no runtime needed)
#   make shellcheck      # lint every shell script
#   make digests         # print the pinned base digests

# Flat repo scheme: ghcr.io/toggle-corp/toggle-web-baker-<image> (matches the
# operator's config.go defaults and the Helm chart image refs).
REGISTRY ?= ghcr.io/toggle-corp/toggle-web-baker
TAG      ?= dev

IMAGES := clone copier du cleanup clock node18 node24

# ---- pinned base image digests (mirror of the FROM lines) -------------------
CLONE_BASE   := alpine/git@sha256:c0280cf9572316299b08544065d3bf35db65043d5e3963982ec50647d2746e26
COPIER_BASE  := alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
DU_BASE      := alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
CLEANUP_BASE := node@sha256:813a7480f28fdadac1f7f5c824bcdad435b5bc1322a5968bbbdef8d058f9dff4
CLOCK_BASE   := alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
NODE18_BASE  := node@sha256:8d6421d663b4c28fd3ebc498332f249011d118945588d0a35cb9bc4b8ca09d9e
NODE24_BASE  := node@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd

.PHONY: build $(IMAGES) test shellcheck digests

build: $(IMAGES)

clone:
	docker build -t $(REGISTRY)-clone:$(TAG) ./clone

copier:
	docker build -t $(REGISTRY)-copier:$(TAG) ./copier

du:
	docker build -t $(REGISTRY)-du:$(TAG) ./du

cleanup:
	docker build -t $(REGISTRY)-cleanup:$(TAG) ./cleanup

clock:
	docker build -t $(REGISTRY)-clock:$(TAG) ./clock

node18:
	docker build -t $(REGISTRY)-node18:$(TAG) ./node18

node24:
	docker build -t $(REGISTRY)-node24:$(TAG) ./node24

test:
	bash ./test/copier_test.sh
	bash ./test/clone_test.sh
	bash ./test/cleanup_test.sh
	bash ./test/clock_test.sh
	bash ./test/node_test.sh

shellcheck:
	shellcheck -x -s bash \
		clone/entrypoint.sh clone/git-askpass.sh \
		copier/entrypoint.sh copier/lib.sh \
		du/entrypoint.sh \
		cleanup/entrypoint.sh \
		clock/entrypoint.sh \
		test/copier_test.sh \
		test/clone_test.sh \
		test/cleanup_test.sh \
		test/clock_test.sh \
		test/node_test.sh

digests:
	@echo "clone:   $(CLONE_BASE)"
	@echo "copier:  $(COPIER_BASE)"
	@echo "du:      $(DU_BASE)"
	@echo "cleanup: $(CLEANUP_BASE)"
	@echo "clock:   $(CLOCK_BASE)"
	@echo "node18:  $(NODE18_BASE)"
	@echo "node24:  $(NODE24_BASE)"
