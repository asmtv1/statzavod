import { Modal } from '../../shared/ui/Primitives'
import { useI18n } from '../../shared/i18n/I18nProvider'
export default function VideoEditor({src,value,onChange,onClose}:{src:string;value:string;onChange:(value:string)=>void;onClose:()=>void}){const {t}=useI18n();return <Modal open title={t('Cover frame')} closeLabel={t('Закрыть')} onClose={onClose}><div><video src={src} controls muted playsInline/><label>{t('Cover frame')}<input type="time" step="1" value={value} onChange={event=>onChange(event.target.value)}/></label></div></Modal>}
