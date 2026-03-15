"""Payment tools: agent-initiated payments with confirmation.

Inspired by google-agentic-commerce/AP2. Implements a two-step
confirmation flow — the tool returns a "confirm?" prompt, and the
user must explicitly confirm before execution.
"""

import json
import time

from agent import tool

# In-memory ledger for demo/dev purposes
_balance = 1000.00
_pending_payments: dict[str, dict] = {}
_transaction_history: list[dict] = []
_payment_counter = 0


@tool(
    "payment_check_balance",
    "Check the current available balance.",
    {
        "type": "object",
        "properties": {},
        "required": [],
    },
)
async def payment_check_balance() -> str:
    return json.dumps({
        "balance": _balance,
        "currency": "USD",
        "pending": len(_pending_payments),
    })


@tool(
    "payment_send",
    "Initiate a payment. Returns a confirmation request that the user must approve. "
    "IMPORTANT: Always ask the user to confirm before calling this tool.",
    {
        "type": "object",
        "properties": {
            "amount": {
                "type": "number",
                "description": "Amount to send",
            },
            "recipient": {
                "type": "string",
                "description": "Recipient identifier (email, wallet address, etc.)",
            },
            "note": {
                "type": "string",
                "description": "Optional payment note",
            },
            "confirmed": {
                "type": "boolean",
                "description": "Set to true only after user has explicitly confirmed the payment",
            },
        },
        "required": ["amount", "recipient"],
    },
)
async def payment_send(
    amount: float,
    recipient: str,
    note: str = "",
    confirmed: bool = False,
) -> str:
    global _balance, _payment_counter

    if amount <= 0:
        return "Invalid amount. Must be positive."

    if amount > _balance:
        return f"Insufficient balance. Available: ${_balance:.2f}, requested: ${amount:.2f}"

    if not confirmed:
        _payment_counter += 1
        payment_id = f"pay_{_payment_counter}"
        _pending_payments[payment_id] = {
            "id": payment_id,
            "amount": amount,
            "recipient": recipient,
            "note": note,
            "created_at": time.time(),
        }
        return (
            f"Payment pending confirmation:\n"
            f"  Amount: ${amount:.2f}\n"
            f"  To: {recipient}\n"
            f"  Note: {note or '(none)'}\n"
            f"  Payment ID: {payment_id}\n\n"
            f"Ask the user to confirm this payment before proceeding."
        )

    # Execute confirmed payment
    _balance -= amount
    tx = {
        "type": "send",
        "amount": amount,
        "recipient": recipient,
        "note": note,
        "timestamp": time.time(),
        "balance_after": _balance,
    }
    _transaction_history.append(tx)

    # Clear any pending
    for pid in list(_pending_payments.keys()):
        p = _pending_payments[pid]
        if p["amount"] == amount and p["recipient"] == recipient:
            del _pending_payments[pid]
            break

    return (
        f"Payment sent successfully!\n"
        f"  Amount: ${amount:.2f}\n"
        f"  To: {recipient}\n"
        f"  Remaining balance: ${_balance:.2f}"
    )
