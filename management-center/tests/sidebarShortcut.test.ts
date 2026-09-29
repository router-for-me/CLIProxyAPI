import { describe, expect, test } from 'bun:test';
import {
  getSidebarShortcutLabel,
  isSidebarToggleShortcut,
} from '@/utils/sidebarShortcut';

describe('Sidebar Shortcut Logic', () => {
  describe('isSidebarToggleShortcut', () => {
    test('matches metaKey + b (Mac Cmd+B)', () => {
      expect(
        isSidebarToggleShortcut({
          key: 'b',
          metaKey: true,
          ctrlKey: false,
        })
      ).toBe(true);

      expect(
        isSidebarToggleShortcut({
          key: 'B',
          metaKey: true,
          ctrlKey: false,
        })
      ).toBe(true);
    });

    test('matches ctrlKey + b (Windows/Linux Ctrl+B)', () => {
      expect(
        isSidebarToggleShortcut({
          key: 'b',
          metaKey: false,
          ctrlKey: true,
        })
      ).toBe(true);

      expect(
        isSidebarToggleShortcut({
          key: 'B',
          metaKey: false,
          ctrlKey: true,
        })
      ).toBe(true);
    });

    test('rejects without metaKey or ctrlKey', () => {
      expect(
        isSidebarToggleShortcut({
          key: 'b',
          metaKey: false,
          ctrlKey: false,
        })
      ).toBe(false);
    });

    test('rejects keys other than b/B', () => {
      expect(
        isSidebarToggleShortcut({
          key: 'c',
          metaKey: true,
          ctrlKey: false,
        })
      ).toBe(false);

      expect(
        isSidebarToggleShortcut({
          key: 'k',
          metaKey: false,
          ctrlKey: true,
        })
      ).toBe(false);
    });

    test('ignores when target is an input, textarea, or contentEditable element', () => {
      const inputEl = {
        tagName: 'INPUT',
        isContentEditable: false,
      } as unknown as HTMLElement;

      const textareaEl = {
        tagName: 'TEXTAREA',
        isContentEditable: false,
      } as unknown as HTMLElement;

      const contentEditableEl = {
        tagName: 'DIV',
        isContentEditable: true,
      } as unknown as HTMLElement;

      const buttonEl = {
        tagName: 'BUTTON',
        isContentEditable: false,
      } as unknown as HTMLElement;

      expect(
        isSidebarToggleShortcut({
          key: 'b',
          metaKey: true,
          ctrlKey: false,
          target: inputEl,
        })
      ).toBe(false);

      expect(
        isSidebarToggleShortcut({
          key: 'b',
          metaKey: true,
          ctrlKey: false,
          target: textareaEl,
        })
      ).toBe(false);

      expect(
        isSidebarToggleShortcut({
          key: 'b',
          metaKey: true,
          ctrlKey: false,
          target: contentEditableEl,
        })
      ).toBe(false);

      expect(
        isSidebarToggleShortcut({
          key: 'b',
          metaKey: true,
          ctrlKey: false,
          target: buttonEl,
        })
      ).toBe(true);
    });
  });

  describe('getSidebarShortcutLabel', () => {
    test('returns ⌘B for Mac and Ctrl+B for non-Mac', () => {
      expect(getSidebarShortcutLabel(true)).toBe('⌘B');
      expect(getSidebarShortcutLabel(false)).toBe('Ctrl+B');
    });
  });
});
