#!/usr/bin/env python3
"""Local-only Snowflake SQL shim.

WORKAROUND for the Snowflake managed-MCP SYSTEM_EXECUTE_SQL bug —
tools/list works, tools/call returns "Error parsing response" when
the JWT is validated through EXTERNAL_OAUTH. The same JWT runs SQL
fine against /api/v2/statements, so this shim fronts that REST API
and Maverics' mcpBridge adapts it back to an MCP tool surface.

This file is LOCAL-ONLY. Do not commit. The bug should be fixed on
Snowflake's side; this exists so we can demo end-to-end today.
"""

import json
import os
import sys
import urllib.error
import urllib.request

from flask import Flask, jsonify, request

app = Flask(__name__)

ACCOUNT_URL = os.environ.get("SNOWFLAKE_ACCOUNT_URL", "").rstrip("/")
DEFAULT_WAREHOUSE = os.environ.get("SNOWFLAKE_WAREHOUSE", "MAVERICS_WH")
DEFAULT_ROLE = os.environ.get("SNOWFLAKE_ROLE", "MAVERICS_DEMO_ROLE")

if not ACCOUNT_URL:
    print("FATAL: SNOWFLAKE_ACCOUNT_URL must be set", file=sys.stderr)
    sys.exit(1)


@app.route("/healthz", methods=["GET"])
def healthz():
    return jsonify({"ok": True, "account_url": ACCOUNT_URL})


@app.route("/sql", methods=["POST"])
def run_sql():
    """Forward a SQL statement to Snowflake's REST API using the inbound JWT.

    The inbound Authorization header is the JWT Maverics' mcpBridge minted
    via RFC 8693 token exchange — audience = Snowflake account URL, scope
    includes `session:role:MAVERICS_DEMO_ROLE`. We pass it through verbatim
    and add the OAUTH token-type header Snowflake requires.
    """
    auth = request.headers.get("Authorization", "")
    if not auth.lower().startswith("bearer "):
        return jsonify({
            "error": "missing_authorization",
            "detail": "Bearer token required",
        }), 401

    body = request.get_json(force=True, silent=True) or {}
    sql = body.get("sql") or body.get("statement")
    if not sql:
        return jsonify({
            "error": "missing_sql",
            "detail": "request body must include `sql`",
        }), 400

    warehouse = body.get("warehouse") or DEFAULT_WAREHOUSE
    role = body.get("role") or DEFAULT_ROLE
    timeout = int(body.get("timeout") or 30)

    req_body = json.dumps({
        "statement": sql,
        "warehouse": warehouse,
        "role": role,
        "timeout": timeout,
    }).encode("utf-8")

    req = urllib.request.Request(
        f"{ACCOUNT_URL}/api/v2/statements",
        data=req_body,
        method="POST",
        headers={
            "Authorization": auth,
            "X-Snowflake-Authorization-Token-Type": "OAUTH",
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )

    try:
        with urllib.request.urlopen(req, timeout=timeout + 5) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode("utf-8"))
        except Exception:
            err_body = {"detail": str(e)}
        return jsonify({
            "error": "snowflake_error",
            "status": e.code,
            "body": err_body,
        }), 502
    except Exception as e:
        return jsonify({"error": "shim_error", "detail": str(e)}), 500

    cols = [c["name"] for c in data.get("resultSetMetaData", {}).get("rowType", [])]
    rows = data.get("data") or []
    rows_out = [dict(zip(cols, r)) for r in rows]

    return jsonify({
        "columns": cols,
        "row_count": len(rows_out),
        "rows": rows_out,
        "statement_handle": data.get("statementHandle"),
        "warehouse": warehouse,
        "role": role,
    })


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=int(os.environ.get("PORT", "5000")))
