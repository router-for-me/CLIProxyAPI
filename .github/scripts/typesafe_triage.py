#!/usr/bin/env python3
"""
TypeSafe Jev automated triage for CLIProxyAPI.
- PR mode: Evaluates newly opened / synchronized PRs immediately.
- Issue batch mode: Evaluates open issues in batches using Jev System One API.
"""
import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

API_KEY = os.environ.get("TYPESAFE_API_KEY")
URL = "https://api.typesafe.ai/v1/systemone"
REPO = os.environ.get("GITHUB_REPOSITORY", "router-for-me/CLIProxyAPI")
DRY_RUN = False

# Load user preference rules
rules_file = os.path.join(os.path.dirname(__file__), "../triage_rules.json")
user_rules = {}
if os.path.exists(rules_file):
    with open(rules_file, "r", encoding="utf-8") as f:
        try:
            user_rules = json.load(f)
        except Exception as e:
            print(f"[Warning] Failed to load triage_rules.json: {e}")

try:
    HIGH_PRIORITY_THRESHOLD = float(user_rules.get("rules", {}).get("high_priority_threshold", 2.5))
except (ValueError, TypeError):
    HIGH_PRIORITY_THRESHOLD = 2.5


def run_gh(args, check=False):
    """Execute a GitHub CLI command and return the result, printing stderr on failure."""
    cmd = ["gh"] + args
    res = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if res.returncode != 0:
        err_msg = res.stderr.strip() if res.stderr else "Unknown error"
        print(f"[Warning] Command failed: {' '.join(cmd)}\nStderr: {err_msg}")
        if check:
            raise subprocess.CalledProcessError(res.returncode, cmd, res.stdout, res.stderr)
    return res


def ensure_label(name, description="", color="ededed"):
    """Ensure a GitHub label exists, creating or updating it if needed."""
    if DRY_RUN:
        return
    run_gh([
        "label", "create", name,
        "--repo", REPO,
        "--description", description,
        "--color", color,
        "--force"
    ])


def call_jev(state, questions):
    """Call TypeSafe Jev System One API with error handling and timeout."""
    payload = {
        "state": state,
        "model": "jev-latest",
        "questions": questions
    }
    req = urllib.request.Request(
        URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {API_KEY}",
            "Content-Type": "application/json"
        }
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        err_body = e.read().decode("utf-8", errors="ignore")
        print(f"[Error] TypeSafe Jev API HTTP Error {e.code}: {e.reason}\nResponse: {err_body}")
        return None
    except urllib.error.URLError as e:
        print(f"[Error] TypeSafe Jev API URL Error: {e.reason}")
        return None
    except Exception as e:
        print(f"[Error] Unexpected error calling TypeSafe Jev API: {e}")
        return None


def handle_pr(pr_number):
    """Evaluate a single PR immediately upon open or update."""
    print(f"--> [PR Mode] Evaluating PR #{pr_number} on {REPO}...")
    res = run_gh([
        "pr", "view", str(pr_number),
        "--repo", REPO,
        "--json", "number,title,body,additions,deletions,changedFiles,author,labels,files,comments"
    ])
    if res.returncode != 0:
        print(f"Failed to fetch PR #{pr_number}")
        return

    try:
        pr_data = json.loads(res.stdout)
    except Exception as e:
        print(f"Failed to parse PR #{pr_number} JSON: {e}")
        return

    body = (pr_data.get("body") or "").strip()
    if len(body) > 600:
        body = body[:600] + "..."

    author = (pr_data.get("author") or {}).get("login", "")
    existing_labels = {
        lbl.get("name")
        for lbl in (pr_data.get("labels") or [])
        if isinstance(lbl, dict) and lbl.get("name")
    }

    files_data = pr_data.get("files") or []
    file_paths = [
        f.get("path", "")
        for f in files_data
        if isinstance(f, dict) and f.get("path")
    ]
    if len(file_paths) > 30:
        files_summary = file_paths[:30] + [f"... and {len(file_paths) - 30} more"]
    else:
        files_summary = file_paths

    state = {
        "user_rules": user_rules,
        "pr": {
            "number": pr_data.get("number", pr_number),
            "title": pr_data.get("title", ""),
            "author": author,
            "diff": f"+{pr_data.get('additions', 0)}/-{pr_data.get('deletions', 0)} in {pr_data.get('changedFiles', 0)} files",
            "files": files_summary,
            "description": body
        }
    }
    questions = {
        "score": {
            "type": "score",
            "instructions": {
                "question": "Rate the merge readiness and value of `pr` for CLIProxyAPI based on `user_rules`.",
                "rule": "Strictly score 0 if it matches `user_rules.ignore_patterns`."
            },
            "criteria": [
                "0: Matches ignore_patterns or invalid/unwanted change",
                "1: Low priority / minor cosmetic / trivial typo",
                "2: Normal feature or valid non-blocking improvement",
                "3: Critical high-priority bugfix, security fix, or core protocol repair"
            ]
        },
        "action": {
            "type": "choice",
            "instructions": "Recommended triage action for this PR?",
            "criteria": {
                "merge_priority": "Critical bugfix or high-value parity fix, prioritize review",
                "needs_review": "Valid feature or normal enhancement, standard review",
                "close_or_reject": "Matches ignored patterns, unwanted, or invalid"
            }
        }
    }

    resp_data = call_jev(state, questions)
    if not resp_data:
        print(f"[Warning] Skipping PR #{pr_number} actions due to Jev API failure.")
        return

    answers = resp_data.get("answers") or {}
    score_ans = answers.get("score") or {}
    action_ans = answers.get("action") or {}

    score_raw = score_ans.get("score")
    if score_raw is None:
        print(f"[Warning] PR #{pr_number}: Jev returned no score in answers. Skipping triage actions.")
        return
    try:
        score = float(score_raw)
    except (ValueError, TypeError):
        print(f"[Warning] PR #{pr_number}: Invalid score value '{score_raw}'. Skipping triage actions.")
        return

    score_conf = float(score_ans.get("confidence") or 0.0)
    action = str(action_ans.get("choice") or "needs_review")
    print(f"PR #{pr_number} Result: Score={score:.2f} (conf={score_conf:.2f}), Action={action}")

    existing_comments = pr_data.get("comments") or []
    has_prior_triage_comment = any(
        "<!-- typesafe-triage -->" in (c.get("body") or "") or "[TypeSafe Triage]" in (c.get("body") or "")
        for c in existing_comments
        if isinstance(c, dict)
    )

    if score < 0.5 or action == "close_or_reject":
        print(f"[Decision] PR #{pr_number} -> INVALID / REJECT (Score: {score:.2f})")
        if DRY_RUN:
            print(f"[Dry-Run] Would add label 'invalid' and post triage comment to PR #{pr_number}")
        else:
            cmd = ["pr", "edit", str(pr_number), "--repo", REPO, "--add-label", "invalid"]
            if "priority-merge" in existing_labels:
                cmd.extend(["--remove-label", "priority-merge"])
            run_gh(cmd)

            if not has_prior_triage_comment or "invalid" not in existing_labels:
                comment_body = (
                    f"<!-- typesafe-triage -->\n"
                    f"🤖 **[TypeSafe Triage]**\n\n"
                    f"- **Score**: `{score:.2f} / 3.0` (Confidence: `{score_conf:.2f}`)\n"
                    f"- **Action**: `close_or_reject`\n"
                    f"- **Note**: This PR matches automated ignore rules (e.g. docs typo, untracked massive port, or out-of-scope change). Marked as low priority."
                )
                run_gh(["pr", "comment", str(pr_number), "--repo", REPO, "--body", comment_body])
    elif score >= HIGH_PRIORITY_THRESHOLD:
        print(f"[Decision] PR #{pr_number} -> MERGE PRIORITY (Score: {score:.2f} >= {HIGH_PRIORITY_THRESHOLD:.2f})")
        ensure_label("priority-merge", "High-priority PR recommended for review/merge", "0e8a16")
        if DRY_RUN:
            print(f"[Dry-Run] Would add label 'priority-merge' and post priority comment to PR #{pr_number}")
        else:
            cmd = ["pr", "edit", str(pr_number), "--repo", REPO, "--add-label", "priority-merge"]
            if "invalid" in existing_labels:
                cmd.extend(["--remove-label", "invalid"])
            run_gh(cmd)

            if not has_prior_triage_comment or "priority-merge" not in existing_labels:
                comment_body = (
                    f"<!-- typesafe-triage -->\n"
                    f"🚀 **[TypeSafe Triage] Priority Merge Candidate**\n\n"
                    f"- **Score**: `{score:.2f} / 3.0` (Confidence: `{score_conf:.2f}`)\n"
                    f"- **Action**: `merge_priority`\n"
                    f"- **Summary**: Identified as a critical bugfix or core protocol repair. Recommended for prioritized maintainer review."
                )
                run_gh(["pr", "comment", str(pr_number), "--repo", REPO, "--body", comment_body])
    else:
        print(f"[Decision] PR #{pr_number} -> NORMAL REVIEW (Score: {score:.2f})")
        if not DRY_RUN:
            labels_to_remove = []
            if "invalid" in existing_labels:
                labels_to_remove.append("invalid")
            if "priority-merge" in existing_labels:
                labels_to_remove.append("priority-merge")
            if labels_to_remove:
                run_gh(["pr", "edit", str(pr_number), "--repo", REPO, "--remove-label", ",".join(labels_to_remove)])


def handle_issue_batch(min_batch=10, max_issues=50):
    """Batch-evaluate issues in chunks using Jev System One API."""
    print(f"--> [Issue Batch Mode] Checking open issues on {REPO} (min_batch={min_batch}, max_issues={max_issues})...")
    known_triage_labels = [
        "triaged", "bug", "invalid", "enhancement", "wontfix",
        "lampoon", "Fixed", "pending", "question", "priority-merge"
    ]
    neg_labels = " ".join(f"-label:{lbl}" for lbl in known_triage_labels)
    search_query = f"is:open is:issue {neg_labels}"
    fetch_limit = max(max_issues * 2, 100)

    res = run_gh([
        "issue", "list", "--repo", REPO,
        "--search", search_query,
        "--limit", str(fetch_limit),
        "--json", "number,title,body,labels"
    ])
    if res.returncode != 0:
        print(f"Failed to fetch issues: {res.stderr}")
        return

    try:
        all_issues = json.loads(res.stdout)
    except Exception as e:
        print(f"Failed to parse issues JSON: {e}")
        return

    known_labels_set = set(known_triage_labels)
    pending = [
        it for it in all_issues
        if not any(
            l.get("name") in known_labels_set
            for l in (it.get("labels") or [])
            if isinstance(l, dict)
        )
    ]

    print(f"Found {len(pending)} open issues without triage labels.")
    if len(pending) < min_batch:
        print(f"Waiting for at least {min_batch} pending issues to trigger batch evaluation (current: {len(pending)}).")
        return

    ensure_label("triaged", "Issue has been triaged", "bfdadc")

    batch_size = 10
    processed = 0

    while pending and processed < max_issues:
        cur_batch_size = min(batch_size, max_issues - processed)
        batch = pending[:cur_batch_size]
        pending = pending[cur_batch_size:]

        print(f"--> Triggering Jev System One for batch of {len(batch)} issues ({processed + 1}..{processed + len(batch)})...")

        state = {"user_rules": user_rules}
        questions = {}

        for it in batch:
            num = str(it["number"])
            body = (it.get("body") or "").strip()
            if len(body) > 400:
                body = body[:400] + "..."
            state[f"issue_{num}"] = {
                "number": it["number"],
                "title": it.get("title", ""),
                "body": body
            }
            questions[f"score_{num}"] = {
                "type": "score",
                "instructions": {
                    "question": f"Rate `issue_{num}` value for CLIProxyAPI based on `user_rules`.",
                    "rule": "Strictly score 0 if it matches `user_rules.ignore_patterns`."
                },
                "criteria": [
                    "0: Matches ignore_patterns, lacks reproduction logs, or invalid complaint",
                    "1: Low priority inquiry",
                    "2: Normal feature request or valid non-blocking issue",
                    "3: Critical real bug (data loss, bad cooldown, stream failure)"
                ]
            }

        resp_data = call_jev(state, questions)
        if not resp_data:
            print("[Warning] Aborting remaining issue batches due to Jev API failure.")
            break

        answers = resp_data.get("answers") or {}

        for it in batch:
            num = str(it["number"])
            score_ans = answers.get(f"score_{num}")
            if not score_ans:
                print(f"[Warning] Issue #{num}: Jev returned no answer for score_{num}. Skipping.")
                continue
            score_raw = score_ans.get("score")
            if score_raw is None:
                print(f"[Warning] Issue #{num}: Jev returned no score value. Skipping.")
                continue
            try:
                sc = float(score_raw)
            except (ValueError, TypeError):
                print(f"[Warning] Issue #{num}: Invalid score '{score_raw}'. Skipping.")
                continue

            conf = float(score_ans.get("confidence") or 0.0)
            print(f"Issue #{num}: Score={sc:.2f} (conf={conf:.2f})")

            if sc < 0.5:
                print(f"[Decision] Issue #{num} -> INVALID / LOW PRIORITY (Score: {sc:.2f})")
                if DRY_RUN:
                    print(f"[Dry-Run] Would add label 'invalid' to Issue #{num} (keep open)")
                else:
                    run_gh(["issue", "edit", num, "--repo", REPO, "--add-label", "invalid"])
                    triage_comment = (
                        f"<!-- typesafe-triage -->\n"
                        f"🤖 **[TypeSafe Triage]**\n\n"
                        f"This issue matches filter rules or lacks required logs/reproduction details (Score: `{sc:.2f}/3.0`).\n"
                        f"Marked as `invalid`. If this is a valid bug or feature request, please update the description with reproduction steps, configurations, or curl commands."
                    )
                    run_gh(["issue", "comment", num, "--repo", REPO, "--body", triage_comment])
            elif sc >= HIGH_PRIORITY_THRESHOLD:
                print(f"[Decision] Issue #{num} -> CRITICAL BUG (Score: {sc:.2f} >= {HIGH_PRIORITY_THRESHOLD:.2f})")
                if DRY_RUN:
                    print(f"[Dry-Run] Would add labels 'bug', 'triaged' to Issue #{num}")
                else:
                    run_gh(["issue", "edit", num, "--repo", REPO, "--add-label", "bug,triaged"])
            else:
                print(f"[Decision] Issue #{num} -> NORMAL TRIAGED (Score: {sc:.2f})")
                if DRY_RUN:
                    print(f"[Dry-Run] Would add label 'triaged' to Issue #{num}")
                else:
                    run_gh(["issue", "edit", num, "--repo", REPO, "--add-label", "triaged"])

        processed += len(batch)


def parse_args():
    parser = argparse.ArgumentParser(
        description="TypeSafe Jev automated triage for CLIProxyAPI."
    )
    parser.add_argument(
        "--dry-run",
        dest="global_dry_run",
        action="store_true",
        default=None,
        help="Perform a dry run without modifying GitHub state."
    )
    subparsers = parser.add_subparsers(dest="command")

    pr_parser = subparsers.add_parser("pr", help="Triage a single PR.")
    pr_parser.add_argument("pr_number", type=int, help="PR number to evaluate.")
    pr_parser.add_argument(
        "--dry-run",
        dest="sub_dry_run",
        action="store_true",
        default=None,
        help="Perform a dry run without modifying GitHub state."
    )

    issue_parser = subparsers.add_parser("issue-batch", help="Batch triage untriaged open issues.")
    issue_parser.add_argument(
        "--min-batch",
        type=int,
        default=10,
        help="Minimum pending issues required to evaluate (default: 10)."
    )
    issue_parser.add_argument(
        "--max-issues",
        type=int,
        default=50,
        help="Maximum issues to process in one run (default: 50)."
    )
    issue_parser.add_argument(
        "--dry-run",
        dest="sub_dry_run",
        action="store_true",
        default=None,
        help="Perform a dry run without modifying GitHub state."
    )

    args = parser.parse_args()
    args.dry_run = bool(
        args.global_dry_run
        or getattr(args, "sub_dry_run", None)
        or os.environ.get("DRY_RUN") == "1"
    )
    return args


if __name__ == "__main__":
    args = parse_args()
    DRY_RUN = args.dry_run

    if not API_KEY:
        print("Warning: TYPESAFE_API_KEY is not set. Exiting.")
        sys.exit(0)

    if args.command == "pr":
        handle_pr(args.pr_number)
    elif args.command == "issue-batch":
        handle_issue_batch(min_batch=args.min_batch, max_issues=args.max_issues)
    else:
        print("No command specified. Use 'pr <pr_number>' or 'issue-batch'.")
        sys.exit(1)
