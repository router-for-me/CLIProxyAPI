/**
 * 两列网格里哪些行要占满整行：显式 wide 的行，以及会落单的行
 * （位于行首、且它是最后一项或下一项占满整行）——否则右侧会空出一格。纯函数，便于测试。
 * SettingList 用它决定给哪些子行注入 wide。
 */
export function resolveRowSpans(wide: readonly boolean[]): boolean[] {
  let column = 0;
  return wide.map((isWide, index) => {
    if (isWide) {
      column = 0;
      return true;
    }
    const orphan = column === 0 && (index === wide.length - 1 || wide[index + 1]);
    column = orphan ? 0 : 1 - column;
    return orphan;
  });
}
