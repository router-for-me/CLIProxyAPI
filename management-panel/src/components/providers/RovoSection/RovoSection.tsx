import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import iconRovo from '@/assets/icons/rovo.svg';
import styles from '@/pages/AiProvidersPage.module.scss';

export function RovoSection() {
  const handleConfigure = () => {
    window.open('/rovo-auth.html', '_blank');
  };

  return (
    <Card
      title={
        <span className={styles.cardTitle}>
          <img src={iconRovo} alt="" className={styles.cardTitleIcon} />
          Atlassian Rovo
        </span>
      }
      extra={
        <Button size="sm" onClick={handleConfigure}>
          Configure Account
        </Button>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
        <p style={{ margin: 0, color: 'var(--text-secondary)', fontSize: '14px' }}>
          Integrate Atlassian Rovo (Bedrock) using the CLI Proxy.
          This allows you to use your Rovo access tokens through this API.
        </p>
        <div style={{ 
          fontSize: '12px', 
          color: 'var(--text-tertiary)',
          backgroundColor: 'var(--bg-secondary)',
          padding: '8px',
          borderRadius: '4px',
          marginTop: '8px'
        }}>
          <strong>Note:</strong> Make sure the Rovo CLI server is running (automatically started via start.bat).
        </div>
      </div>
    </Card>
  );
}
