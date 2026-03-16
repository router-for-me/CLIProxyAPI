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


def _format_pending_payment(payment: dict) -> str:
    return (
        f"Payment pending confirmation:\n"
        f"  Amount: ${payment['amount']:.2f}\n"
        f"  To: {payment['recipient']}\n"
        f"  Note: {payment['note'] or '(none)'}\n"
        f"  Payment ID: {payment['id']}\n\n"
        "Ask the user to confirm this payment before proceeding."
    )


@tool(
    "payment_send",
    "Create or confirm a payment. First call with amount and recipient to create a pending payment. "
    "Then call with payment_id and confirmed=true after the user explicitly confirms.",
    {
        "type": "object",
        "properties": {
            "amount": {
                "type": "number",
                "description": "Amount to send when creating a new pending payment",
            },
            "recipient": {
                "type": "string",
                "description": "Recipient identifier when creating a new pending payment",
            },
            "note": {
                "type": "string",
                "description": "Optional payment note",
            },
            "payment_id": {
                "type": "string",
                "description": "Pending payment ID returned by a previous call. Required when confirmed=true.",
            },
            "confirmed": {
                "type": "boolean",
                "description": "Use false to create a pending payment; use true with payment_id to confirm it.",
            },
        },
        "required": [],
    },
)
async def payment_send(
    amount: float | None = None,
    recipient: str = "",
    note: str = "",
    payment_id: str = "",
    confirmed: bool = False,
) -> str:
    global _balance, _payment_counter

    if confirmed:
        payment_id = payment_id.strip()
        if not payment_id:
            return "Payment confirmation requires a payment_id."

        payment = _pending_payments.get(payment_id)
        if payment is None:
            return f"Payment '{payment_id}' not found or already processed."

        if payment["amount"] > _balance:
            return f"Insufficient balance. Available: ${_balance:.2f}, requested: ${payment['amount']:.2f}"

        _balance -= payment["amount"]
        tx = {
            "type": "send",
            "payment_id": payment_id,
            "amount": payment["amount"],
            "recipient": payment["recipient"],
            "note": payment["note"],
            "timestamp": time.time(),
            "balance_after": _balance,
        }
        _transaction_history.append(tx)
        del _pending_payments[payment_id]

        return (
            f"Payment sent successfully!\n"
            f"  Amount: ${payment['amount']:.2f}\n"
            f"  To: {payment['recipient']}\n"
            f"  Payment ID: {payment_id}\n"
            f"  Remaining balance: ${_balance:.2f}"
        )

    recipient = recipient.strip()
    if amount is None or not recipient:
        return "Payment creation requires amount and recipient."

    if amount <= 0:
        return "Invalid amount. Must be positive."

    if amount > _balance:
        return f"Insufficient balance. Available: ${_balance:.2f}, requested: ${amount:.2f}"

    _payment_counter += 1
    payment_id = f"pay_{_payment_counter}"
    payment = {
        "id": payment_id,
        "amount": amount,
        "recipient": recipient,
        "note": note,
        "created_at": time.time(),
    }
    _pending_payments[payment_id] = payment
    return _format_pending_payment(payment)
