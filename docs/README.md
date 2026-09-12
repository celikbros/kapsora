# KAPSORA documentation

Start with the [handover](HANDOVER.md). It records the project state and working rules at
the handover date. Read the documents for the task you are doing; a complete read of every
work package or historical specification is not required to get started.

## Find a document

| Need | Document |
| --- | --- |
| Project state, open decisions and working rules | [Handover](HANDOVER.md) |
| Milestones, work packages and delivery history | [Roadmap](plan/ROADMAP.md) |
| Product scope, users and language | [Product brief](../PRODUCT.md) |
| UI patterns and design rules | [Design system](../DESIGN.md) |
| Development rules and work-package process | [Developer handbook](delegation/README.md) |
| Work-package specifications and delivery reports | [Work packages](delegation/) · [Report template](delegation/REPORT_TEMPLATE.md) |
| Approved scope changes from the original specification | [Master plan v2.0](plan/KAPSORA_Master_Plan_v2.0.md) |
| Architecture decisions and superseded choices | [ADR index](adr/README.md) |
| Frontend packages and commands | [Web workspace](development/frontend.md) |
| Native local dependencies | [Local environment](runbooks/local-native-environment.md) |
| Local user accounts | [Accounts](runbooks/local-accounts.md) |
| Single-server installation | [Deployment procedure](runbooks/deploy-single-server.md) |
| Server layout, ports and release procedure | [Deployment layout](runbooks/deployment-layout.md) |
| Service units and hardening | [systemd reference](runbooks/systemd.md) |
| Backup and recovery | [Backup and restore](runbooks/backup-restore.md) |
| Original specification and historical artifacts | [Frozen v1.2 baseline](baseline-v1.2/) |
| API contract | [OpenAPI](../api/openapi/kapsora-v1.yaml) |

## Keep the documents organized

- Put project documentation under `docs/`: plans in `plan/`, decisions in `adr/`, work
  packages and reports in `delegation/`, developer guides in `development/`, and operational
  procedures in `runbooks/`. Add a link here when adding a new kind of document.
- Update the existing document for an established subject. Keep one authoritative copy;
  link to it instead of making another summary of the same rules.
- Keep `baseline-v1.2/` frozen. Completed work packages and delivery logs are historical
  evidence, not instructions to restart completed work. Read older plans alongside the
  later approved ADRs and the handover; dated CI results are not a current test run.
- Keep the repository `README.md` as the entry point. `PRODUCT.md` and `DESIGN.md` stay at
  the repository root because Impeccable discovers them there.
- Keep tool-owned Markdown in its required location: `.claude/skills/` contains the
  installed skill and its references, `.impeccable/` contains tool state and surface briefs,
  and `.github/` contains GitHub templates. They are not duplicate project guides.
- Write new documentation under `docs/` in English. Preserve the language and content of
  historical documents. Unless a document says otherwise, command examples run from the
  repository root and paths in code spans are relative to that root.
