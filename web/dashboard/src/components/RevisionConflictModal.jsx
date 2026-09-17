// RevisionConflictModal — surfaced when the runtime-config management
// API returns 409 with `error: "revision_conflict"`. The dashboard gives
// the operator three options:
//
//   1. View current  — reload the active snapshot (operator's edits are
//                       discarded; the form refetches).
//   2. Discard mine   — same as View current, but kept as a separate
//                       label so the operator's intent is unambiguous in
//                       support tickets.
//   3. Force save     — re-submit the edit with the new active revision
//                       as the expected_revision. The server still
//                       validates; if another save lands in between, the
//                       cycle repeats until one side yields.
//
// We render `force save` and `discard mine` as the primary actions; the
// close button is the secondary.
import React from 'react';
import { Modal } from './Primitives.jsx';

export default function RevisionConflictModal({
  conflict,        // { activeRevision, expected } | null
  onClose,
  onDiscard,
  onForceSave,
}) {
  if (!conflict) return null;
  return (
    <Modal
      title="Runtime config has changed on the server"
      size="md"
      onClose={onClose}
      footer={(
        <div className="row row--gap">
          <button onClick={onDiscard}>Discard my changes</button>
          <button className="btn--primary" onClick={onForceSave}>
            Force save (revision {conflict.activeRevision})
          </button>
          <button onClick={onClose} style={{ marginLeft: 'auto' }}>
            Close
          </button>
        </div>
      )}
    >
      <p style={{ marginTop: 0 }}>
        Another operator saved a new revision while you were editing. Your
        expected revision was <code>{conflict.expected}</code>; the active
        revision is now <code>{conflict.activeRevision}</code>.
      </p>
      <p style={{ color: 'var(--text-dim)' }}>
        Choose <strong>Discard my changes</strong> to load the published
        values into the form, or <strong>Force save</strong> to overwrite
        the server revision with your draft (subsequent operators will see
        your revision as the new active one).
      </p>
    </Modal>
  );
}
