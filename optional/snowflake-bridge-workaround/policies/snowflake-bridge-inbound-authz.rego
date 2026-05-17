package orchestrator

# LOCAL-ONLY workaround OPA policy. Do not commit.
# Default-allow — matches the employee-directory bridge style.
# Real authorization is enforced by Snowflake's RBAC + masking against
# the caller's identity in the exchanged JWT.

default result["allowed"] := true
