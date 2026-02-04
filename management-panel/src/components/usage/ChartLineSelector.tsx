import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import styles from '@/pages/UsagePage.module.scss';

export interface ChartLineSelectorProps {
  chartLines: string[];
  modelNames: string[];
  maxLines?: number;
  onChange: (lines: string[]) => void;
}

export function ChartLineSelector({
  chartLines,
  modelNames,
  maxLines = 9,
  onChange
}: ChartLineSelectorProps) {
  const { t } = useTranslation();

  const handleAdd = () => {
    if (chartLines.length >= maxLines) return;
    const unusedModel = modelNames.find((m) => !chartLines.includes(m));
    if (unusedModel) {
      onChange([...chartLines, unusedModel]);
    } else {
      onChange([...chartLines, 'all']);
    }
  };

  const handleRemove = (index: number) => {
    if (chartLines.length <= 1) return;
    const newLines = [...chartLines];
    newLines.splice(index, 1);
    onChange(newLines);
  };

  const handleChange = (index: number, value: string) => {
    const newLines = [...chartLines];
    newLines[index] = value;
    onChange(newLines);
  };

  return (
    <Card
      title={t('usage_stats.chart_line_actions_label')}
      extra={
        <div className={styles.chartLineHeader}>
          <span className={styles.chartLineCount}>
            {chartLines.length}/{maxLines}
          </span>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleAdd}
            disabled={chartLines.length >= maxLines}
          >
            {t('usage_stats.chart_line_add')}
          </Button>
        </div>
      }
    >
      <div className={styles.chartLineList}>
        {chartLines.map((line, index) => (
          <div key={index} className={styles.chartLineItem}>
            <span className={styles.chartLineLabel}>
              {t(`usage_stats.chart_line_label_${index + 1}`)}
            </span>
            <select
              value={line}
              onChange={(e) => handleChange(index, e.target.value)}
              className={styles.select}
            >
              <option value="all">{t('usage_stats.chart_line_all')}</option>
              {modelNames.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
            {chartLines.length > 1 && (
              <Button variant="danger" size="sm" onClick={() => handleRemove(index)}>
                {t('usage_stats.chart_line_delete')}
              </Button>
            )}
          </div>
        ))}
      </div>
      <p className={styles.chartLineHint}>{t('usage_stats.chart_line_hint')}</p>
    </Card>
  );
}
