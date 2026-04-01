import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Text } from '@codemirror/state';
import { Chunk } from '@codemirror/merge';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import styles from './DiffModal.module.scss';

type DiffModalProps = {
  open: boolean;
  original: string;
  modified: string;
  onConfirm: () => void;
  onCancel: () => void;
  loading?: boolean;
};

type UnifiedLineType = 'context' | 'addition' | 'deletion';

type UnifiedLine = {
  type: UnifiedLineType;
  oldNum: number | null;
  newNum: number | null;
  text: string;
};

type Hunk = {
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
  lines: UnifiedLine[];
};

type DiffResult = {
  hunks: Hunk[];
  additions: number;
  deletions: number;
};

const DIFF_CONTEXT_LINES = 3;

const clampPos = (doc: Text, pos: number) => Math.max(0, Math.min(pos, doc.length));

function computeUnifiedDiff(original: string, modified: string): DiffResult {
  const oldDoc = Text.of(original.split('\n'));
  const newDoc = Text.of(modified.split('\n'));
  const chunks = Chunk.build(oldDoc, newDoc);

  let totalAdditions = 0;
  let totalDeletions = 0;

  const hunks: Hunk[] = chunks.map((chunk: Chunk) => {
    const lines: UnifiedLine[] = [];

    const hasDel = chunk.fromA < chunk.toA;
    const hasAdd = chunk.fromB < chunk.toB;

    // Collect deleted lines from old doc
    const delLines: { num: number; text: string }[] = [];
    if (hasDel) {
      const startLine = oldDoc.lineAt(chunk.fromA).number;
      const endLine = oldDoc.lineAt(chunk.toA - 1).number;
      for (let i = startLine; i <= endLine; i++) {
        delLines.push({ num: i, text: oldDoc.line(i).text });
      }
    }

    // Collect added lines from new doc
    const addLines: { num: number; text: string }[] = [];
    if (hasAdd) {
      const startLine = newDoc.lineAt(chunk.fromB).number;
      const endLine = newDoc.lineAt(chunk.toB - 1).number;
      for (let i = startLine; i <= endLine; i++) {
        addLines.push({ num: i, text: newDoc.line(i).text });
      }
    }

    totalDeletions += delLines.length;
    totalAdditions += addLines.length;

    // Compute context boundaries
    let ctxBeforeEndOld: number;
    let ctxAfterStartOld: number;
    let ctxBeforeEndNew: number;
    let ctxAfterStartNew: number;

    if (hasDel) {
      ctxBeforeEndOld = delLines[0].num - 1;
      ctxAfterStartOld = delLines[delLines.length - 1].num + 1;
    } else {
      const anchorPos = clampPos(oldDoc, chunk.fromA);
      const lineInfo = oldDoc.lineAt(anchorPos);
      if (chunk.fromA === lineInfo.from) {
        ctxBeforeEndOld = lineInfo.number - 1;
        ctxAfterStartOld = lineInfo.number;
      } else {
        ctxBeforeEndOld = lineInfo.number;
        ctxAfterStartOld = lineInfo.number + 1;
      }
    }

    if (hasAdd) {
      ctxBeforeEndNew = addLines[0].num - 1;
      ctxAfterStartNew = addLines[addLines.length - 1].num + 1;
    } else {
      const anchorPos = clampPos(newDoc, chunk.fromB);
      const lineInfo = newDoc.lineAt(anchorPos);
      if (chunk.fromB === lineInfo.from) {
        ctxBeforeEndNew = lineInfo.number - 1;
        ctxAfterStartNew = lineInfo.number;
      } else {
        ctxBeforeEndNew = lineInfo.number;
        ctxAfterStartNew = lineInfo.number + 1;
      }
    }

    // Context before
    const ctxBeforeCount = Math.min(
      DIFF_CONTEXT_LINES,
      Math.max(0, ctxBeforeEndOld),
      Math.max(0, ctxBeforeEndNew)
    );

    for (let i = ctxBeforeCount; i > 0; i--) {
      const oldNum = ctxBeforeEndOld - i + 1;
      const newNum = ctxBeforeEndNew - i + 1;
      if (oldNum >= 1 && newNum >= 1 && oldNum <= oldDoc.lines) {
        lines.push({
          type: 'context',
          oldNum,
          newNum,
          text: oldDoc.line(oldNum).text
        });
      }
    }

    // Deletions
    for (const del of delLines) {
      lines.push({ type: 'deletion', oldNum: del.num, newNum: null, text: del.text });
    }

    // Additions
    for (const add of addLines) {
      lines.push({ type: 'addition', oldNum: null, newNum: add.num, text: add.text });
    }

    // Context after
    const ctxAfterCountOld = Math.max(
      0,
      Math.min(DIFF_CONTEXT_LINES, oldDoc.lines - ctxAfterStartOld + 1)
    );
    const ctxAfterCountNew = Math.max(
      0,
      Math.min(DIFF_CONTEXT_LINES, newDoc.lines - ctxAfterStartNew + 1)
    );
    const ctxAfterCount = Math.min(ctxAfterCountOld, ctxAfterCountNew);

    for (let i = 0; i < ctxAfterCount; i++) {
      const oldNum = ctxAfterStartOld + i;
      const newNum = ctxAfterStartNew + i;
      if (oldNum >= 1 && oldNum <= oldDoc.lines && newNum >= 1 && newNum <= newDoc.lines) {
        lines.push({
          type: 'context',
          oldNum,
          newNum,
          text: oldDoc.line(oldNum).text
        });
      }
    }

    // Compute hunk header values
    const firstOld = lines.find((l) => l.oldNum !== null)?.oldNum ?? 1;
    const firstNew = lines.find((l) => l.newNum !== null)?.newNum ?? 1;
    const oldCount = lines.filter((l) => l.type !== 'addition').length;
    const newCount = lines.filter((l) => l.type !== 'deletion').length;

    return { oldStart: firstOld, oldCount, newStart: firstNew, newCount, lines };
  });

  return { hunks, additions: totalAdditions, deletions: totalDeletions };
}

const STAT_BLOCKS = 5;

function StatBar({ additions, deletions }: { additions: number; deletions: number }) {
  const total = additions + deletions;
  if (total === 0) return null;
  const addBlocks = Math.round((additions / total) * STAT_BLOCKS);
  return (
    <span className={styles.statBar}>
      {Array.from({ length: STAT_BLOCKS }, (_, i) => (
        <span
          key={i}
          className={`${styles.statBlock} ${i < addBlocks ? styles.statBlockAdd : styles.statBlockDel}`}
        />
      ))}
    </span>
  );
}

export function DiffModal({
  open,
  original,
  modified,
  onConfirm,
  onCancel,
  loading = false
}: DiffModalProps) {
  const { t } = useTranslation();

  const diff = useMemo<DiffResult>(
    () => computeUnifiedDiff(original, modified),
    [original, modified]
  );

  return (
    <Modal
      open={open}
      title={t('config_management.diff.title')}
      onClose={onCancel}
      width="min(1200px, 90vw)"
      className={styles.diffModal}
      closeDisabled={loading}
      footer={
        <>
          <Button variant="secondary" onClick={onCancel} disabled={loading}>
            {t('common.cancel')}
          </Button>
          <Button onClick={onConfirm} loading={loading} disabled={loading}>
            {t('config_management.diff.confirm')}
          </Button>
        </>
      }
    >
      <div className={styles.content}>
        {diff.hunks.length === 0 ? (
          <div className={styles.emptyState}>{t('config_management.diff.no_changes')}</div>
        ) : (
          <div className={styles.diffContainer}>
            <div className={styles.fileHeader}>
              <svg className={styles.fileIcon} viewBox="0 0 16 16" width="16" height="16">
                <path
                  fillRule="evenodd"
                  d="M3.75 1.5a.25.25 0 00-.25.25v11.5c0 .138.112.25.25.25h8.5a.25.25 0 00.25-.25V6H9.75A1.75 1.75 0 018 4.25V1.5H3.75zm5.75.56v2.19c0 .138.112.25.25.25h2.19L9.5 2.06zM2 1.75C2 .784 2.784 0 3.75 0h5.086c.464 0 .909.184 1.237.513l3.414 3.414c.329.328.513.773.513 1.237v8.086A1.75 1.75 0 0112.25 15h-8.5A1.75 1.75 0 012 13.25V1.75z"
                  fill="currentColor"
                />
              </svg>
              <span className={styles.fileName}>config.yaml</span>
              <span className={styles.fileStats}>
                <span className={styles.statAdditions}>+{diff.additions}</span>
                <span className={styles.statDeletions}>-{diff.deletions}</span>
                <StatBar additions={diff.additions} deletions={diff.deletions} />
              </span>
            </div>

            <div className={styles.diffBody}>
              {diff.hunks.map((hunk, hunkIdx) => (
                <div key={hunkIdx} className={styles.hunk}>
                  <div className={styles.hunkHeader}>
                    <span className={styles.hunkGutter}>
                      <svg
                        className={styles.hunkExpandIcon}
                        viewBox="0 0 16 16"
                        width="12"
                        height="12"
                      >
                        <path
                          d="M8.177 1.677l2.896 2.896a.25.25 0 01-.177.427H8.75v1.25a.75.75 0 01-1.5 0V5H5.104a.25.25 0 01-.177-.427l2.896-2.896a.25.25 0 01.354 0zM7.25 11.75a.75.75 0 011.5 0V13h2.146a.25.25 0 01.177.427l-2.896 2.896a.25.25 0 01-.354 0l-2.896-2.896A.25.25 0 015.104 13H7.25v-1.25z"
                          fill="currentColor"
                        />
                      </svg>
                    </span>
                    <span className={styles.hunkGutter} />
                    <span className={styles.hunkText}>
                      @@ -{hunk.oldStart},{hunk.oldCount} +{hunk.newStart},{hunk.newCount} @@
                    </span>
                  </div>

                  {hunk.lines.map((line, lineIdx) => (
                    <div
                      key={`${hunkIdx}-${lineIdx}`}
                      className={`${styles.diffLine} ${styles[line.type]}`}
                    >
                      <span
                        className={`${styles.lineNum} ${line.oldNum === null ? styles.lineNumEmpty : ''}`}
                      >
                        {line.oldNum ?? ''}
                      </span>
                      <span
                        className={`${styles.lineNum} ${line.newNum === null ? styles.lineNumEmpty : ''}`}
                      >
                        {line.newNum ?? ''}
                      </span>
                      <span className={styles.linePrefix}>
                        {line.type === 'deletion' ? '-' : line.type === 'addition' ? '+' : ' '}
                      </span>
                      <code className={styles.lineText}>{line.text || ' '}</code>
                    </div>
                  ))}
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
