import { useState } from 'react'
import ChangeWorkspace from './ChangeWorkspace'
import TaskWorkspace from './TaskWorkspace'
import DeliveryPackages from './DeliveryPackages'

export default function DeliveryWorkspace() {
  const [view, setView] = useState<'changes' | 'review' | 'packages'>('changes')
  return <>
    <nav className="workspace-navigation" aria-label="工作台导航">
      <button type="button" aria-pressed={view === 'changes'} onClick={() => setView('changes')}>需求交付</button>
      <button type="button" aria-pressed={view === 'review'} onClick={() => setView('review')}>单仓库审阅</button>
      <button type="button" aria-pressed={view === 'packages'} onClick={() => setView('packages')}>交付包</button>
    </nav>
    {view === 'changes' ? <ChangeWorkspace /> : view === 'review' ? <TaskWorkspace /> : <DeliveryPackages />}
  </>
}
